package server

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/reconcile"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// requestGrants asks the approval backend for a grant per gate target and records
// each in the store with the backend's grant name + state. A backend error
// records the target "blocked" (so the verdict stays unsatisfied and the live
// page can surface it) rather than failing finalize. No-op without a backend.
//
// One requester identity is leased for the whole PR: the first successful grant
// response that carries a Requester sets it, and every subsequent gate of the
// same PR is requested with that identity pinned. After the loop the requester
// (if any) is persisted on all gate_target rows for the PR.
func (a *App) requestGrants(ctx context.Context, pr int, environment, repo string, gates []events.GateTarget) {
	if a.Approval == nil {
		return
	}
	var leased string
	for _, gt := range gates {
		g, err := a.tryRequestGrant(ctx, approval.Request{
			Class: gt.Class, Target: gt.Target, PR: pr, Environment: environment,
			Requester: leased,
		}, repo)
		if err != nil {
			log.Printf("gate: request grant pr=%d env=%s %s/%s: %v", pr, environment, gt.Class, gt.Target, err)
			_ = store.UpsertTarget(a.db, pr, environment, gt.Class, gt.Target, "", "blocked")
			continue
		}
		if leased == "" && g.Requester != "" {
			leased = g.Requester
		}
		if uerr := store.UpsertTarget(a.db, pr, environment, gt.Class, gt.Target, g.Name, string(g.State)); uerr != nil {
			log.Printf("gate: record target pr=%d env=%s %s/%s: %v", pr, environment, gt.Class, gt.Target, uerr)
		}
	}
	if leased != "" {
		if err := store.SetTargetRequester(a.db, pr, environment, leased); err != nil {
			log.Printf("gate: set requester pr=%d env=%s: %v", pr, environment, err)
		}
	}
}

// tryRequestGrant calls RequestGrant and, on a SlotCollisionError, checks
// whether the blocking PR is closed via GitHub. If closed, revokes the blocking
// grant and retries once. If open (or the GitHub check fails), logs the blocker
// and returns the original error so the target is recorded "blocked".
func (a *App) tryRequestGrant(ctx context.Context, req approval.Request, repo string) (approval.Grant, error) {
	g, err := a.Approval.RequestGrant(ctx, req)
	if err == nil {
		return g, nil
	}
	var colErr *approval.SlotCollisionError
	if !errors.As(err, &colErr) {
		return approval.Grant{}, err
	}
	blocker := colErr.BlockingGrant
	abandoned, cerr := a.gh.PRAbandoned(ctx, repo, blocker.Request.PR)
	if cerr != nil {
		log.Printf("gate: slot-collision check PR #%d: %v", blocker.Request.PR, cerr)
		return approval.Grant{}, err
	}
	if !abandoned {
		log.Printf("gate: slot occupied by open/merged PR #%d env=%s on %s/%s — waiting",
			blocker.Request.PR, blocker.Request.Environment, req.Class, req.Target)
		return approval.Grant{}, err
	}
	if rerr := a.Approval.Revoke(ctx, blocker.Request); rerr != nil {
		log.Printf("gate: revoke blocker PR #%d: %v", blocker.Request.PR, rerr)
		return approval.Grant{}, err
	}
	log.Printf("gate: auto-revoked orphaned grant from abandoned PR #%d, retrying", blocker.Request.PR)
	return a.Approval.RequestGrant(ctx, req)
}

// revokeOrphans revokes every open grant stored for pr across all environments.
// Called from the PR-closed webhook. Errors are logged but not propagated.
func (a *App) revokeOrphans(ctx context.Context, pr int) {
	if a.cfg.ReconcilerCore && a.shell != nil {
		targets, err := store.PRTargets(a.db, pr)
		if err != nil {
			log.Printf("webhook: load targets for PR #%d: %v", pr, err)
			return
		}
		envs := map[string]bool{}
		for _, t := range targets {
			envs[t.Environment] = true
		}
		for env := range envs {
			if herr := a.shell.Handle(ctx, pr, env, "", reconcile.PRClosed{}); herr != nil {
				log.Printf("reconcile-core: pr-closed pr=%d env=%s: %v", pr, env, herr)
			}
		}
		return
	}
	if a.Approval == nil {
		return
	}
	targets, err := store.PRTargets(a.db, pr)
	if err != nil {
		log.Printf("webhook: load targets for PR #%d: %v", pr, err)
		return
	}
	for _, t := range targets {
		if err := a.Approval.Revoke(ctx, approval.Request{
			Class:       t.Class,
			Target:      t.Target,
			PR:          pr,
			Environment: t.Environment,
		}); err != nil {
			log.Printf("webhook: revoke PR #%d env=%s %s/%s: %v",
				pr, t.Environment, t.Class, t.Target, err)
		}
	}
}

// matchGrantState returns the state of the open grant matching (pr, environment),
// preferring ACTIVE; "" when there is no matching open grant.
func matchGrantState(grants []approval.Grant, pr int, environment string) approval.GrantState {
	var found approval.GrantState
	for _, g := range grants {
		if g.Request.PR != pr || g.Request.Environment != environment || !g.State.Open() {
			continue
		}
		if g.State == approval.StateActive {
			return approval.StateActive
		}
		found = g.State
	}
	return found
}

// reconcileGate refreshes each stored gate target's state from the backend and,
// once every target is ACTIVE, marks the gate active, flips its gated stacks to
// safe, and re-drives the check run terminally (conclusion → success). Self-heals
// the activating→active transition and any missed provider events.
//
// Returns a non-nil error when the reconcile could NOT confirm current grant
// state (a backend/list failure). Callers that gate apply on the result MUST
// treat that as fail-closed — never fall back to the (possibly stale) cache. A
// successful reconcile, no backend, or no targets returns nil.
func (a *App) reconcileGate(ctx context.Context, pr int, environment string) error {
	if a.cfg.ReconcilerCore && a.shell != nil {
		return a.shell.tick(ctx, pr, environment)
	}
	if a.Approval == nil {
		return nil
	}
	targets, err := store.TargetsFor(a.db, pr, environment)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		return nil
	}
	allActive := true
	var reconcileErr error
	// A later target's ListGrants failure returns an error after the loop, so the
	// gate-check fails closed. Any earlier target already refreshed via UpsertTarget
	// is a harmless partial — MarkActive is skipped, and the next successful
	// reconcile fully refreshes both. (The core path's tick is all-or-nothing.)
	for _, t := range targets {
		grants, lerr := a.Approval.ListGrants(ctx, t.Class, t.Target)
		if lerr != nil {
			// Could not confirm this target — remember it and surface to the caller
			// so an apply gate-check fails closed instead of trusting the cache.
			allActive = false
			reconcileErr = lerr
			continue
		}
		st := matchGrantState(grants, pr, environment)
		if st != "" {
			if uerr := store.UpsertTarget(a.db, pr, environment, t.Class, t.Target, t.GrantName, string(st)); uerr != nil {
				log.Printf("gate: refresh target pr=%d env=%s %s/%s: %v", pr, environment, t.Class, t.Target, uerr)
			}
		}
		if st != approval.StateActive {
			allActive = false
		}
	}
	if reconcileErr != nil {
		return reconcileErr
	}
	if !allActive {
		return nil
	}
	if err := store.MarkActive(a.db, pr, environment); err != nil {
		log.Printf("gate: mark active pr=%d env=%s: %v", pr, environment, err)
	}
	if _, err := a.db.Exec(
		`UPDATE stacks SET status = ? WHERE status = ?
		   AND execution_id IN (SELECT id FROM executions WHERE pr = ? AND environment = ?)`,
		string(events.StatusSafe), string(events.StatusGated), pr, environment); err != nil {
		log.Printf("gate: flip gated stacks pr=%d env=%s: %v", pr, environment, err)
	}
	if id, ok := store.LatestExecutionID(a.db, pr, environment); ok {
		a.drive(ctx, id, strings.TrimRight(a.cfg.PublicBaseURL, "/"), true)
	}
	return nil
}

// reconcilePending re-evaluates every gate that is not yet fully ACTIVE.
func (a *App) reconcilePending(ctx context.Context) {
	if a.Approval == nil {
		return
	}
	pending, err := store.PendingGates(a.db)
	if err != nil {
		log.Printf("reconcile: list pending gates: %v", err)
		return
	}
	for _, g := range pending {
		if err := a.reconcileGate(ctx, g.PR, g.Environment); err != nil {
			log.Printf("reconcile: gate pr=%d env=%s: %v", g.PR, g.Environment, err)
		}
	}
}

// handleGateCheck is the apply-time, fail-closed gate pre-check: 200 only when
// the (pr, environment) was classified AND every recorded gate target is ACTIVE
// (a classified plan with no gates passes). A never-planned PR, an unsatisfied
// gate, or any error → 409/5xx, so apply blocks. Reconciles first to catch a
// just-approved gate.
func (a *App) handleGateCheck(w http.ResponseWriter, r *http.Request) {
	var p events.GateCheck
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		badRequest(w, err)
		return
	}
	classified, err := store.IsClassified(a.db, p.PR, p.Environment)
	if err != nil {
		http.Error(w, "classified check", http.StatusInternalServerError)
		return
	}
	if !classified {
		http.Error(w, "not classified", http.StatusConflict)
		return
	}
	if err := a.reconcileGate(r.Context(), p.PR, p.Environment); err != nil {
		// Could not freshly confirm the gate (e.g. PAM unreachable). Fail closed —
		// never authorize apply from a possibly-stale cache. 503 = transient/retriable.
		http.Error(w, "gate state could not be confirmed", http.StatusServiceUnavailable)
		return
	}
	targets, err := store.TargetsFor(a.db, p.PR, p.Environment)
	if err != nil {
		http.Error(w, "load targets", http.StatusInternalServerError)
		return
	}
	for _, t := range targets {
		if t.State != string(approval.StateActive) {
			http.Error(w, "gate not satisfied", http.StatusConflict)
			return
		}
	}
	requester := ""
	if len(targets) > 0 {
		requester = targets[0].Requester
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"requester": requester})
}

// handleGateRevoke revokes the grants the server requested for (pr, environment)
// — best-effort post-apply cleanup. No-op without a backend.
func (a *App) handleGateRevoke(w http.ResponseWriter, r *http.Request) {
	var p events.GateRevoke
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		badRequest(w, err)
		return
	}
	if a.cfg.ReconcilerCore && a.shell != nil {
		if err := a.shell.Handle(r.Context(), p.PR, p.Environment, "", reconcile.ApplySucceeded{}); err != nil {
			log.Printf("reconcile-core: gate revoke pr=%d env=%s: %v", p.PR, p.Environment, err)
		}
		w.WriteHeader(http.StatusOK)
		return
	}
	if a.Approval != nil {
		targets, _ := store.TargetsFor(a.db, p.PR, p.Environment)
		for _, t := range targets {
			if err := a.Approval.Revoke(r.Context(), approval.Request{
				Class: t.Class, Target: t.Target, PR: p.PR, Environment: p.Environment,
			}); err != nil {
				log.Printf("gate: revoke pr=%d env=%s %s/%s: %v", p.PR, p.Environment, t.Class, t.Target, err)
			}
		}
	}
	w.WriteHeader(http.StatusOK)
}

// sweepOrphanedGrants revokes grants left open on a closed/merged PR that escaped
// the close webhook and post-apply cleanup. For each PR still holding an open
// grant it checks the PR's GitHub state and, if closed, runs the same revoke path
// as the webhook (revokeOrphans). Conservative on uncertainty: a PRClosed error
// leaves the grant alone — never revoke an open PR's grant on a transient blip.
// Best-effort and idempotent; safe to run periodically.
func (a *App) sweepOrphanedGrants(ctx context.Context) {
	prs, err := store.OpenGrantPRs(a.db)
	if err != nil {
		log.Printf("sweep: list open-grant PRs: %v", err)
		return
	}
	for _, p := range prs {
		if p.Repo == "" {
			// No execution on record → no repo to ask GitHub about. Skip (can't
			// confirm closed); the next reconcile/finalize re-establishes the repo.
			log.Printf("sweep: PR #%d has open grants but no execution repo — skipping", p.PR)
			continue
		}
		abandoned, cerr := a.gh.PRAbandoned(ctx, p.Repo, p.PR)
		if cerr != nil {
			log.Printf("sweep: PRAbandoned(repo=%s pr=%d): %v", p.Repo, p.PR, cerr)
			continue
		}
		if abandoned {
			log.Printf("sweep: PR #%d (repo=%s) is abandoned but holds open grants — revoking", p.PR, p.Repo)
			a.revokeOrphans(ctx, p.PR)
		}
	}
}

// OrphanSweepLoop periodically runs sweepOrphanedGrants until ctx is cancelled.
// Slower cadence than ReconcileLoop: it calls the GitHub API per open-grant PR
// and orphan cleanup is not time-critical. No-op without an approval backend or
// GitHub client.
func (a *App) OrphanSweepLoop(ctx context.Context, interval time.Duration) {
	if a.Approval == nil || a.gh == nil {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.sweepOrphanedGrants(ctx)
		}
	}
}

// ReconcileLoop periodically re-evaluates every not-yet-ACTIVE gate, so a grant
// that goes ACTIVE after the request (the common case) converges to success even
// with no provider event. No-op without a backend. Blocks until ctx is cancelled.
func (a *App) ReconcileLoop(ctx context.Context, interval time.Duration) {
	if a.Approval == nil {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.reconcilePending(ctx)
		}
	}
}
