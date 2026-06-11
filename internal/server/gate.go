package server

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
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
func (a *App) requestGrants(ctx context.Context, pr int, environment string, gates []events.GateTarget) {
	if a.Approval == nil {
		return
	}
	var leased string
	for _, gt := range gates {
		g, err := a.Approval.RequestGrant(ctx, approval.Request{
			Class: gt.Class, Target: gt.Target, PR: pr, Environment: environment,
			Requester: leased,
		})
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
// the activating→active transition and any missed provider events. No-op without
// a backend or stored targets.
func (a *App) reconcileGate(ctx context.Context, pr int, environment string) {
	if a.Approval == nil {
		return
	}
	targets, err := store.TargetsFor(a.db, pr, environment)
	if err != nil || len(targets) == 0 {
		return
	}
	allActive := true
	for _, t := range targets {
		grants, err := a.Approval.ListGrants(ctx, t.Class, t.Target)
		if err != nil {
			allActive = false
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
	if !allActive {
		return
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
		a.reconcileGate(ctx, g.PR, g.Environment)
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
	a.reconcileGate(r.Context(), p.PR, p.Environment)
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
