package server

import (
	"encoding/json"
	"net/http"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/reconcile"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// handleClaimsList returns all apply-lock claims for an environment.
// POST body: {"environment":"<env>"}
// Response: JSON array of events.Claim (snake_case fields) (200); 404 when ApplyLock is off.
func (a *App) handleClaimsList(w http.ResponseWriter, r *http.Request) {
	if !a.cfg.ApplyLock {
		http.NotFound(w, r)
		return
	}
	var req struct {
		Environment string `json:"environment"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		badRequest(w, err)
		return
	}
	claims, err := store.ListClaims(a.db, req.Environment)
	if err != nil {
		http.Error(w, "list claims", http.StatusInternalServerError)
		return
	}
	// Convert to events.Claim so the wire uses snake_case json tags, matching
	// what the runner client (ClaimsList) decodes into []events.Claim.
	// Return an empty JSON array (never null) when there are no claims.
	out := make([]events.Claim, 0, len(claims))
	for _, c := range claims {
		out = append(out, events.Claim{
			Environment: c.Environment,
			StackPath:   c.StackPath,
			OwnerPR:     c.OwnerPR,
			ExpiresAt:   c.ExpiresAt,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// handleClaimsRelease admin-releases one stack's claim or all of a PR's claims.
// POST body: {"environment":"<env>","pr":<n>,"stack":"<optional>"}
// Response: 200; 404 when ApplyLock is off.
func (a *App) handleClaimsRelease(w http.ResponseWriter, r *http.Request) {
	if !a.cfg.ApplyLock {
		http.NotFound(w, r)
		return
	}
	var req struct {
		Environment string `json:"environment"`
		PR          int    `json:"pr"`
		Stack       string `json:"stack"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		badRequest(w, err)
		return
	}
	a.adminReleaseClaims(r.Context(), req.Environment, req.PR, req.Stack)
	w.WriteHeader(http.StatusOK)
}

func (a *App) handleInit(w http.ResponseWriter, r *http.Request) {
	var in events.Init
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		badRequest(w, err)
		return
	}
	if err := store.UpsertInit(a.db, in); err != nil {
		http.Error(w, "store init", http.StatusInternalServerError)
		return
	}
	base := a.baseURL(r)
	if isGate(in.Context, in.Environment) && a.cfg.UseChecks {
		if err := a.ensureCheckRun(r.Context(), in.ID, in.Repo, in.SHA, checkRunName(in.Environment), a.liveURL(base, in.ID)); err != nil {
			http.Error(w, "create check run", http.StatusBadGateway)
			return
		}
	}
	if isApplyContext(in.Context) && a.cfg.UseChecks {
		if err := a.ensureCheckRun(r.Context(), in.ID, in.Repo, in.SHA, in.Context, a.liveURL(base, in.ID)); err != nil {
			http.Error(w, "create check run", http.StatusBadGateway)
			return
		}
	}
	if isApplyContext(in.Context) && a.cfg.ApplyLock {
		_ = store.AssociateClaimExecution(a.db, in.Environment, in.PR, in.ID)
	}
	a.drive(r.Context(), in.ID, base, false)
	w.WriteHeader(http.StatusOK)
}

func (a *App) handlePhase(w http.ResponseWriter, r *http.Request) {
	var p events.PhaseEvent
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		badRequest(w, err)
		return
	}
	if err := store.UpsertPhase(a.db, p); err != nil {
		http.Error(w, "store phase", http.StatusInternalServerError)
		return
	}
	e, err := store.GetExecution(a.db, p.ID)
	if err != nil {
		http.Error(w, "read execution", http.StatusInternalServerError)
		return
	}
	base := a.baseURL(r)
	if isGate(e.StatusContext, e.Environment) && a.cfg.UseChecks {
		if err := a.ensureCheckRun(r.Context(), e.ID, e.Repo, e.SHA, checkRunName(e.Environment), a.liveURL(base, e.ID)); err != nil {
			http.Error(w, "create check run", http.StatusBadGateway)
			return
		}
	}
	if isApplyContext(e.StatusContext) && a.cfg.UseChecks {
		if err := a.ensureCheckRun(r.Context(), e.ID, e.Repo, e.SHA, e.StatusContext, a.liveURL(base, e.ID)); err != nil {
			http.Error(w, "create check run", http.StatusBadGateway)
			return
		}
	}
	if isApplyContext(e.StatusContext) && a.cfg.ApplyLock {
		a.renewApplyClaims(e.Environment, e.PR)
	}
	a.drive(r.Context(), p.ID, base, false)
	w.WriteHeader(http.StatusOK)
}

func (a *App) handleUpdate(w http.ResponseWriter, r *http.Request) {
	var u events.Update
	if err := json.NewDecoder(r.Body).Decode(&u); err != nil {
		badRequest(w, err)
		return
	}
	if err := store.UpdateStack(a.db, u.ID, u.Stack, u.Status, u.Detail); err != nil {
		http.Error(w, "update stack", http.StatusInternalServerError)
		return
	}
	if a.Objects != nil && done(u.Status) {
		_ = a.offloadLog(r.Context(), u.ID, u.Stack)
	}
	if a.cfg.ApplyLock {
		if ue, err := store.GetExecution(a.db, u.ID); err == nil && isApplyContext(ue.StatusContext) {
			a.renewApplyClaims(ue.Environment, ue.PR)
		}
	}
	a.drive(r.Context(), u.ID, a.baseURL(r), false)
	w.WriteHeader(http.StatusOK)
}

func (a *App) handleFinalize(w http.ResponseWriter, r *http.Request) {
	var f events.Finalize
	if err := json.NewDecoder(r.Body).Decode(&f); err != nil {
		badRequest(w, err)
		return
	}
	if err := store.SetReport(a.db, f.ID, f.ReportMarkdown); err != nil {
		http.Error(w, "store report", http.StatusInternalServerError)
		return
	}
	// Persist each stack's rendered plan section (kind='plan') for the per-stack
	// Plan tab. Markdown is stored inline in the excerpt.
	for path, md := range f.StackReports {
		if err := store.UpsertStackOutput(a.db, f.ID, path, "plan", "", md); err != nil {
			http.Error(w, "store stack plan", http.StatusInternalServerError)
			return
		}
	}
	e, err := store.GetExecution(a.db, f.ID)
	if err != nil {
		http.Error(w, "read execution", http.StatusInternalServerError)
		return
	}

	if f.Failed {
		// A stack still pending/running at a failed finalize did not itself fail —
		// terramate aborted the run (e.g. a parallel sibling 403'd) before it
		// reached a terminal tick. Mark it `aborted`, not `failed`, so innocent /
		// no-change stacks are not mislabeled. The run-level failure conclusion
		// comes from the persisted execution status below, not from counting
		// failed stacks — so a true orchestrator crash with zero per-stack
		// failures still concludes failure.
		if _, err := a.db.Exec(
			`UPDATE stacks SET status = ? WHERE execution_id = ? AND status IN (?, ?, ?, ?)`,
			string(events.StatusAborted), f.ID, string(events.StatusPending), string(events.StatusRunning),
			string(events.StatusInitializing), string(events.StatusInitialized)); err != nil {
			http.Error(w, "mark aborted", http.StatusInternalServerError)
			return
		}
		if err := store.SetExecutionStatus(a.db, f.ID, "failure"); err != nil {
			http.Error(w, "set failure", http.StatusInternalServerError)
			return
		}
		a.drive(r.Context(), f.ID, a.baseURL(r), true)
		if g, gerr := store.LoadGraph(a.db, f.ID); gerr == nil {
			a.finalizeLogs(r.Context(), f.ID, g.Stacks)
		}
		if isApplyContext(e.StatusContext) && a.cfg.ApplyLock {
			a.releaseApplyClaims(r.Context(), e.Environment, e.PR)
		}
		w.WriteHeader(http.StatusOK)
		return
	}

	// Backfill per-stack target/grouping key from the finalize payload.
	for path, project := range f.Projects {
		if _, err := a.db.Exec(
			`UPDATE stacks SET project = ? WHERE execution_id = ? AND stack_path = ?`,
			project, f.ID, path); err != nil {
			http.Error(w, "backfill project", http.StatusInternalServerError)
			return
		}
	}

	// Backfill per-stack matched categories (for the group-DAG badges).
	for path, cats := range f.Categories {
		data, _ := json.Marshal(cats)
		if _, err := a.db.Exec(
			`UPDATE stacks SET categories = ? WHERE execution_id = ? AND stack_path = ?`,
			string(data), f.ID, path); err != nil {
			http.Error(w, "backfill categories", http.StatusInternalServerError)
			return
		}
	}

	// Backfill per-stack operation counts (for the blast-radius bar + op summaries).
	for path, c := range f.Counts {
		data, _ := json.Marshal(c)
		if _, err := a.db.Exec(
			`UPDATE stacks SET counts = ? WHERE execution_id = ? AND stack_path = ?`,
			string(data), f.ID, path); err != nil {
			http.Error(w, "backfill counts", http.StatusInternalServerError)
			return
		}
	}

	// Record the gate targets and mark gated stacks. In reconciler-core mode the
	// Shell handles grant requests, state recording, and stack status updates.
	// In legacy mode the original inline logic applies.
	if a.cfg.ReconcilerCore && a.shell != nil {
		if err := a.shell.Handle(r.Context(), e.PR, e.Environment, e.Repo, reconcile.RunnerFinalize{Gates: f.Gates}); err != nil {
			http.Error(w, "reconcile finalize", http.StatusInternalServerError)
			return
		}
	} else {
		// Record the gate targets. With a backend, request a grant per target (it
		// records the grant name + live state); without one, record AWAITING so the
		// verdict still parks at action_required. Either way, collect the targets so
		// the matching stacks can be marked gated.
		if a.Approval != nil {
			a.requestGrants(r.Context(), e.PR, e.Environment, e.Repo, f.Gates)
		} else {
			for _, gt := range f.Gates {
				if err := store.UpsertTarget(a.db, e.PR, e.Environment, gt.Class, gt.Target, "", "AWAITING"); err != nil {
					http.Error(w, "record gate", http.StatusInternalServerError)
					return
				}
			}
		}
		gatedTargets := map[string]bool{}
		for _, gt := range f.Gates {
			gatedTargets[gt.Target] = true
		}
		for target := range gatedTargets {
			if _, err := a.db.Exec(
				`UPDATE stacks SET status = ? WHERE execution_id = ? AND project = ? AND status != ?`,
				string(events.StatusGated), f.ID, target, string(events.StatusFailed)); err != nil {
				http.Error(w, "mark gated", http.StatusInternalServerError)
				return
			}
		}
	}

	// Mark moving stacks (adopting resources via a cross-state move) — non-gating,
	// only changes the node from safe to moving. Skip stacks already gated/failed.
	for _, path := range f.Moving {
		if _, err := a.db.Exec(
			`UPDATE stacks SET status = ? WHERE execution_id = ? AND stack_path = ? AND status NOT IN (?, ?)`,
			string(events.StatusMoving), f.ID, path, string(events.StatusGated), string(events.StatusFailed)); err != nil {
			http.Error(w, "mark moving", http.StatusInternalServerError)
			return
		}
	}

	if !(a.cfg.ReconcilerCore && a.shell != nil) {
		if err := store.MarkClassified(a.db, e.PR, e.Environment); err != nil {
			http.Error(w, "mark classified", http.StatusInternalServerError)
			return
		}
		// Drive terminally — AFTER gate targets are stored, so the conclusion sees them.
		a.drive(r.Context(), f.ID, a.baseURL(r), true)
	}
	if g, gerr := store.LoadGraph(a.db, f.ID); gerr == nil {
		a.finalizeLogs(r.Context(), f.ID, g.Stacks)
	}
	if a.cfg.ApplyLock {
		if isApplyContext(e.StatusContext) {
			a.releaseApplyClaims(r.Context(), e.Environment, e.PR)
		} else if e.PR > 0 {
			// Plan finalize: the PR's changed stacks are now registered, so post
			// apply-lock/<env> here. The pull_request webhook fires on PR open —
			// before the plan registers the stacks — so this is what makes the
			// check appear (alongside plan/<env>), enabling the auto-merge gate.
			a.postPlanApplyLock(r.Context(), e)
		}
	}
	w.WriteHeader(http.StatusOK)
}
