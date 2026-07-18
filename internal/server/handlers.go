package server

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/Fluent-Health/terraform-stack-plan/internal/catalog"
	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/execution"
	"github.com/Fluent-Health/terraform-stack-plan/internal/reconcile"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// handleClaimsList returns all apply-lock claims for an environment.
// POST body: {"environment":"<env>"}
// Response: JSON array of events.Claim (snake_case fields) (200).
func (a *App) handleClaimsList(w http.ResponseWriter, r *http.Request) {
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
// Response: 200.
func (a *App) handleClaimsRelease(w http.ResponseWriter, r *http.Request) {
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
	// Normalize the gate context at write: the runner sends "" for the plan
	// gate while serve-queued executions carry "plan/<env>" — both must land in
	// one supersede bucket (FindNonSupersededExecution matches on the stored
	// context), or a runner init can never supersede its serve-queued twin.
	if isGate(in.Context, in.Environment) {
		in.Context = statusContext(in.Environment)
	}
	// A rerun created outside triggers.run can lose its _PR_NUMBER, so its runner
	// reports pr=0 and would orphan (FindNonSupersededExecution short-circuits at
	// pr<=0). Recover the owning PR from an existing non-superseded execution for
	// the same (env, context, sha) — the key the inbound-build path also uses — and
	// backfill it before the row is written, so the supersede below reattaches the
	// rerun to the PR's check.
	// Safe because PR-triggered builds always set _PR_NUMBER and push builds
	// recover-or-skip a PR upstream, so a genuinely PR-less Init never collides
	// with a real PR's (env, context, sha).
	if in.PR <= 0 && in.SHA != "" {
		if id, ok, ferr := store.FindExecutionBySHA(a.db, in.Environment, in.Context, in.SHA); ferr == nil && ok {
			if e, gerr := store.GetExecution(a.db, id); gerr == nil && e.PR > 0 {
				in.PR = e.PR
			}
		}
	}
	if err := a.shell.HandleExec(r.Context(), in.ID, execution.ReportInit{Exec: execInitFromEvents(in)}); err != nil {
		http.Error(w, "store init", http.StatusInternalServerError)
		return
	}
	if in.PR > 0 {
		oldID, found, err := store.FindNonSupersededExecution(a.db, in.PR, in.Environment, in.SHA, in.Context, in.ID)
		if err == nil && found {
			// Direction-guard: never mark a chronologically newer execution superseded by an older one.
			// Re-triggered manual builds may reuse an older execution ID (defaults in trigger substitutions),
			// which would incorrectly supersede the newer run.
			oldExec, err1 := store.GetExecution(a.db, oldID)
			inExec, err2 := store.GetExecution(a.db, in.ID)
			if err1 == nil && err2 == nil {
				if oldExec.CreatedAt.After(time.Time{}) && inExec.CreatedAt.Before(oldExec.CreatedAt) {
					// Inverted: the incoming execution is strictly older than the existing one in the DB!
					a.supersedeExecution(r.Context(), in.ID, oldID)
				} else {
					// Default: incoming execution is newer or same time (e.g. test harness identical times)
					a.supersedeExecution(r.Context(), oldID, in.ID)
				}
			} else {
				a.supersedeExecution(r.Context(), oldID, in.ID)
			}
		}
	}
	base := a.baseURL(r)
	if isGate(in.Context, in.Environment) {
		if err := a.ensureCheckRun(r.Context(), in.ID, in.Repo, in.SHA, a.planCheckName(in.Environment), a.uiURL(in.PR, in.ID)); err != nil {
			http.Error(w, "create check run", http.StatusBadGateway)
			return
		}
	}
	if isApplyContext(in.Context) {
		if err := a.ensureCheckRun(r.Context(), in.ID, in.Repo, in.SHA, in.Context, a.uiURL(in.PR, in.ID)); err != nil {
			http.Error(w, "create check run", http.StatusBadGateway)
			return
		}
	}
	if isApplyContext(in.Context) {
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
	if err := a.shell.HandleExec(r.Context(), p.ID, execution.ReportPhase{
		ID: p.ID, Phase: p.Phase, Label: p.Label, Pct: p.ProgressPct,
		Repo: p.Repo, SHA: p.SHA, PR: p.PR, Environment: p.Environment, Context: p.Context, LogURL: p.LogURL,
	}); err != nil {
		http.Error(w, "store phase", http.StatusInternalServerError)
		return
	}
	e, err := store.GetExecution(a.db, p.ID)
	if err != nil {
		http.Error(w, "read execution", http.StatusInternalServerError)
		return
	}
	base := a.baseURL(r)
	if isGate(e.StatusContext, e.Environment) {
		if err := a.ensureCheckRun(r.Context(), e.ID, e.Repo, e.SHA, a.planCheckName(e.Environment), a.uiURL(e.PR, e.ID)); err != nil {
			http.Error(w, "create check run", http.StatusBadGateway)
			return
		}
	}
	if isApplyContext(e.StatusContext) {
		if err := a.ensureCheckRun(r.Context(), e.ID, e.Repo, e.SHA, e.StatusContext, a.uiURL(e.PR, e.ID)); err != nil {
			http.Error(w, "create check run", http.StatusBadGateway)
			return
		}
	}
	if isApplyContext(e.StatusContext) {
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
	if err := a.shell.HandleExec(r.Context(), u.ID, execution.ReportTick{Stack: u.Stack, Status: u.Status, Detail: u.Detail}); err != nil {
		http.Error(w, "update stack", http.StatusInternalServerError)
		return
	}
	if a.Objects != nil && done(u.Status) {
		_ = a.offloadLog(r.Context(), u.ID, u.Stack)
	}
	if ue, err := store.GetExecution(a.db, u.ID); err == nil && isApplyContext(ue.StatusContext) {
		a.renewApplyClaims(ue.Environment, ue.PR)
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
	if err := store.SetChangeReasons(a.db, f.ID, f.ChangeReasons); err != nil {
		http.Error(w, "store change reasons", http.StatusInternalServerError)
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
		// reached a terminal tick. ReportFail's fold marks it `aborted`, not
		// `failed`, so innocent / no-change stacks are not mislabeled, and sets the
		// run-level status to "failure" regardless of per-stack counts — so a true
		// orchestrator crash with zero per-stack failures still concludes failure.
		if err := a.shell.HandleExec(r.Context(), f.ID, execution.ReportFail{}); err != nil {
			http.Error(w, "reconcile fail", http.StatusInternalServerError)
			return
		}
		a.drive(r.Context(), f.ID, a.baseURL(r), true)
		if g, gerr := store.LoadGraph(a.db, f.ID); gerr == nil {
			a.finalizeLogs(r.Context(), f.ID, g.Stacks)
		}
		// Note: the merge-lock claim is NOT released here. A Finalize is also
		// emitted mid-apply by the classify pass; releasing on it dropped the lock
		// before the apply ran. Release is driven by the apply-end GateRevoke
		// (→ reconcile.ApplySucceeded → ReleaseClaim), which fires only when the
		// apply truly ends. A failed apply still sends GateRevoke; a classify-fail
		// abort keeps the claim until the TTL sweep (safe direction).
		w.WriteHeader(http.StatusOK)
		return
	}

	// Annotate per-stack target/grouping key, matched categories, operation counts,
	// and moving (cross-state-move) overlay from the finalize payload — folded by
	// the execution aggregate and projected in one pass.
	if err := a.shell.HandleExec(r.Context(), f.ID, execution.ReportAnnotate{
		Projects: f.Projects, Categories: f.Categories, Counts: f.Counts, Moving: f.Moving,
	}); err != nil {
		http.Error(w, "annotate stacks", http.StatusInternalServerError)
		return
	}
	// A non-failed finalize is terminal for plan and verify runs — persist the
	// run-level success or the execution reads in_progress forever (lifecycle
	// bar stuck on its last phase, PRs list stuck "planning"). Apply contexts are
	// excluded: the classify pass emits a mid-apply Finalize; driveApply owns
	// their terminal status.
	if !isApplyContext(e.StatusContext) {
		if err := a.shell.HandleExec(r.Context(), f.ID, execution.ReportSucceed{}); err != nil {
			http.Error(w, "reconcile succeed", http.StatusInternalServerError)
			return
		}
	}

	// Record the gate targets, request grants, and mark gated stacks — all handled
	// by the reconcile core's RunnerFinalize transition. Runs LAST among the
	// stack-status writers above: every execution-aggregate HandleExec call
	// (annotate, succeed) reprojects each stack's status column from its
	// runner-told RunStatus, which knows nothing about gating — running the gate
	// overlay after them lets it have the final word on which stacks read
	// `gated`/`safe`, matching pre-cutover behavior where the gate call's direct
	// SQL UPDATE was the last writer to stacks.status for this finalize. The
	// gate overlay also targets stacks by `project`, so the annotate backfill
	// above (which populates `project`) must land first for it to match any rows.
	if err := a.shell.Handle(r.Context(), e.PR, e.Environment, e.Repo, reconcile.RunnerFinalize{
		Gates:        f.Gates,
		ApplyContext: isApplyContext(e.StatusContext),
	}); err != nil {
		http.Error(w, "reconcile finalize", http.StatusInternalServerError)
		return
	}

	if g, gerr := store.LoadGraph(a.db, f.ID); gerr == nil {
		a.finalizeLogs(r.Context(), f.ID, g.Stacks)
	}
	if !isApplyContext(e.StatusContext) && e.PR > 0 && !a.runTriggerArmed() {
		// Legacy two-check mode only: consolidated tiers render the lock
		// verdict inside terraform/<env> during the terminal RenderCheckRun.
		//
		// Plan finalize: the PR's changed stacks are now registered, so post
		// apply-lock/<env> here. The pull_request webhook fires on PR open —
		// before the plan registers the stacks — so this is what makes the
		// check appear (alongside plan/<env>), enabling the auto-merge gate.
		//
		// An apply-context finalize does NOT release the claim here: a Finalize is
		// also emitted mid-apply by the classify pass. Release is driven by the
		// apply-end GateRevoke (→ reconcile.ApplySucceeded → ReleaseClaim).
		a.postPlanApplyLock(r.Context(), e)
	}
	w.WriteHeader(http.StatusOK)
}

// handleGetCatalog builds and returns the pre-aggregated component catalog.
func (a *App) handleGetCatalog(w http.ResponseWriter, r *http.Request) {
	cat, err := catalog.Build(".")
	if err != nil {
		http.Error(w, "build catalog: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(cat)
}
