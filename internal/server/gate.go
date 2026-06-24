package server

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/Fluent-Health/terraform-stack-plan/internal/codes"
	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/reconcile"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// revokeOrphans drives the reconcile core's PR-closed transition for every
// environment the PR holds gate targets in (releasing its grants). Called from
// the PR-closed webhook. Errors are logged but not propagated.
func (a *App) revokeOrphans(ctx context.Context, pr int) {
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
	return a.shell.tick(ctx, pr, environment)
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

// codedError writes a stable {code,message} JSON error with the given status.
// HTTP status carries the coarse class; the code is the precise, machine-
// readable condition the runner switches on.
func codedError(w http.ResponseWriter, status int, code codes.Code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": string(code), "message": msg})
}

// writeGateOK writes a 200 gate-check response with the given requester SA
// (empty string for a clean/no-gate pass).
func writeGateOK(w http.ResponseWriter, requester string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"requester": requester})
}

// handleGateCheck is the apply-time, fail-closed gate pre-check: 200 only when
// the (pr, environment) was classified AND every recorded gate target is ACTIVE
// (a classified plan with no gates passes). A never-planned PR, an unsatisfied
// gate, or any error → 409/5xx, so apply blocks. Reconciles first to catch a
// just-approved gate, then reads the replayed gate state (lossless truth).
func (a *App) handleGateCheck(w http.ResponseWriter, r *http.Request) {
	var p events.GateCheck
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		badRequest(w, err)
		return
	}
	// Refresh from the backend first (self-heals activating→active). tick is a
	// no-op when there are no targets, so never-planned / clean PRs reconcile
	// cleanly and do not 503.
	if err := a.reconcileGate(r.Context(), p.PR, p.Environment); err != nil {
		// Could not freshly confirm the gate (e.g. PAM unreachable). Fail closed —
		// never authorize apply from a possibly-stale cache. 503 = transient/retriable.
		codedError(w, http.StatusServiceUnavailable, codes.GateUnconfirmable, "gate state could not be confirmed")
		return
	}
	// Read gate AFTER reconcile so a just-approved gate is reflected.
	gate, err := a.shell.loadGate(p.PR, p.Environment)
	if err != nil {
		codedError(w, http.StatusInternalServerError, codes.Internal, "load gate failed")
		return
	}
	switch g := gate.(type) {
	case reconcile.NotClassified:
		codedError(w, http.StatusConflict, codes.GateNotClassified, "not classified")
	case reconcile.Clean:
		writeGateOK(w, "")
	case reconcile.Satisfied:
		writeGateOK(w, g.Lease.Requester)
	default: // Pending / Blocked
		codedError(w, http.StatusConflict, codes.GateNotSatisfied, "gate not satisfied")
	}
}

// handleGateRevoke revokes the grants the server requested for (pr, environment)
// — best-effort post-apply cleanup. No-op without a backend.
func (a *App) handleGateRevoke(w http.ResponseWriter, r *http.Request) {
	var p events.GateRevoke
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		badRequest(w, err)
		return
	}
	if err := a.shell.Handle(r.Context(), p.PR, p.Environment, "", reconcile.ApplySucceeded{}); err != nil {
		log.Printf("reconcile-core: gate revoke pr=%d env=%s: %v", p.PR, p.Environment, err)
	}
	w.WriteHeader(http.StatusOK)
}

// sweepOrphanedGrants revokes grants left open on an abandoned (closed-unmerged)
// PR that escaped the close webhook. For each PR still holding an open grant it
// checks the PR's GitHub state and, if abandoned, runs the same revoke path as
// the webhook (revokeOrphans). A merged PR's grant is left alone — its post-merge
// apply needs it (released by ApplySucceeded; PAM TTL backstops). Conservative on
// uncertainty: a PRAbandoned error leaves the grant alone — never revoke on a
// transient blip. Best-effort and idempotent; safe to run periodically.
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
