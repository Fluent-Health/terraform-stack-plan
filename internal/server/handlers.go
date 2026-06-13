package server

import (
	"encoding/json"
	"net/http"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

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
		if err := a.ensureCheckRun(r.Context(), in.ID, in.Repo, in.SHA, checkRunName(in.Environment), liveURL(base, in.ID)); err != nil {
			http.Error(w, "create check run", http.StatusBadGateway)
			return
		}
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
		if err := a.ensureCheckRun(r.Context(), e.ID, e.Repo, e.SHA, checkRunName(e.Environment), liveURL(base, e.ID)); err != nil {
			http.Error(w, "create check run", http.StatusBadGateway)
			return
		}
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
		// Mark every not-yet-terminal stack failed so the conclusion is failure
		// even when the failure was orchestrator-level (no per-stack tick fired).
		if _, err := a.db.Exec(
			`UPDATE stacks SET status = ? WHERE execution_id = ? AND status IN (?, ?)`,
			string(events.StatusFailed), f.ID, string(events.StatusPending), string(events.StatusRunning)); err != nil {
			http.Error(w, "mark failed", http.StatusInternalServerError)
			return
		}
		a.drive(r.Context(), f.ID, a.baseURL(r), true)
		// Re-offloads stacks handleUpdate already offloaded at terminal status —
		// intentional: the full sweep is what licenses deleting the buffer dir.
		if g, gerr := store.LoadGraph(a.db, f.ID); gerr == nil {
			a.finalizeLogs(r.Context(), f.ID, g.Stacks)
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

	// Record the gate targets. With a backend, request a grant per target (it
	// records the grant name + live state); without one, record AWAITING so the
	// verdict still parks at action_required. Either way, collect the targets so
	// the matching stacks can be marked gated.
	if a.Approval != nil {
		a.requestGrants(r.Context(), e.PR, e.Environment, f.Gates)
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

	if err := store.MarkClassified(a.db, e.PR, e.Environment); err != nil {
		http.Error(w, "mark classified", http.StatusInternalServerError)
		return
	}

	// Drive terminally — AFTER gate targets are stored, so the conclusion sees them.
	a.drive(r.Context(), f.ID, a.baseURL(r), true)
	if g, gerr := store.LoadGraph(a.db, f.ID); gerr == nil {
		a.finalizeLogs(r.Context(), f.ID, g.Stacks)
	}
	w.WriteHeader(http.StatusOK)
}
