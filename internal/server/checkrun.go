package server

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// ensureCheckRun creates the GitHub check run for an execution if it does not yet
// have one, and persists the id. Idempotent: a no-op when check_run_id is set, so
// an early phase event and a later init can both call it safely.
func (a *App) ensureCheckRun(ctx context.Context, id, repo, sha, environment, detailsURL string) error {
	e, err := store.GetExecution(a.db, id)
	if err != nil {
		return err
	}
	if e.CheckRunID.Valid && e.CheckRunID.Int64 != 0 {
		return nil
	}
	crID, err := a.gh.CreateCheckRun(ctx, repo, sha, environment, detailsURL)
	if err != nil {
		return err
	}
	if err := store.SetCheckRunID(a.db, id, crID); err != nil {
		return fmt.Errorf("persist check_run_id: %w", err)
	}
	return nil
}

// renderAndPatch bumps the cache-bust rev and patches the GitHub check run with a
// fresh summary/report. terminal=true also computes the verdict conclusion from
// the DB snapshot (callers MUST patch terminally only after gate targets are
// stored). A no-op in link mode (reconcile owns the commit status there).
func (a *App) renderAndPatch(ctx context.Context, id, base string, terminal bool) {
	if err := store.BumpRev(a.db, id); err != nil {
		log.Printf("rev bump %s: %v", id, err)
		return
	}
	if !a.cfg.UseChecks {
		return
	}
	e, err := store.GetExecution(a.db, id)
	if err != nil {
		log.Printf("load execution %s: %v", id, err)
		return
	}
	if !e.CheckRunID.Valid || e.CheckRunID.Int64 == 0 {
		return
	}
	g, err := store.LoadGraph(a.db, id)
	if err != nil {
		log.Printf("load graph %s: %v", id, err)
		return
	}
	// Surface pending approval gates at the top of the check run so an
	// action_required conclusion is self-explanatory (which gate, how to approve).
	targets, _ := store.TargetsFor(a.db, e.PR, e.Environment)
	upd := CheckRunUpdate{
		Summary:    renderProgress(g),
		Text:       gatesSection(targets) + failuresSection(g, e.LogURL) + e.ReportMarkdown,
		DetailsURL: liveURL(base, id),
	}
	if terminal {
		if snap, _, ok := loadSnapshot(a.db, id); ok {
			upd.Conclusion = conclusion(snap)
		}
	}
	if err := a.gh.UpdateCheckRun(ctx, e.Repo, e.CheckRunID.Int64, upd); err != nil {
		log.Printf("update check run %s: %v", id, err)
	}
}

// reconcile is the link-mode commit-status writer: it projects the execution's
// DB state onto the per-environment plan-gate status and posts it. Best-effort.
func (a *App) reconcile(ctx context.Context, id, base string) {
	snap, e, ok := loadSnapshot(a.db, id)
	if !ok || e.Repo == "" {
		return
	}
	st := gateStatus(snap)
	if err := a.gh.PostStatus(ctx, e.Repo, e.SHA, statusContext(e.Environment), st.state, st.desc, liveURL(base, id)); err != nil {
		log.Printf("reconcile status %s: %v", id, err)
	}
}

// isApplyContext reports whether a status context is a post-merge apply (e.g.
// "apply/nonprod") rather than the plan gate.
func isApplyContext(ctx string) bool {
	return ctx == "apply" || strings.HasPrefix(ctx, "apply/")
}

// driveApply posts/updates the apply/<env> commit status for a post-merge apply
// execution. Apply runs are not check-run gates (a check run on the default
// branch would dangle), so serve surfaces them as a commit status linking to the
// live page. State derives from the stacks: any failed ⇒ failure; all terminal
// (safe) ⇒ success; otherwise pending (still applying). Best-effort.
func (a *App) driveApply(ctx context.Context, e store.Execution, base string) {
	g, err := store.LoadGraph(a.db, e.ID)
	if err != nil {
		log.Printf("apply status: load graph %s: %v", e.ID, err)
		return
	}
	total := len(g.Stacks)
	done, failed := 0, 0
	for _, s := range g.Stacks {
		switch s.Status {
		case events.StatusSafe:
			done++
		case events.StatusFailed:
			failed++
		}
	}
	var state, desc string
	switch {
	case failed > 0:
		state, desc = "failure", fmt.Sprintf("apply failed — %d/%d applied, %d failed", done, total, failed)
	case total > 0 && done == total:
		state, desc = "success", fmt.Sprintf("applied %d/%d stacks", done, total)
	default:
		state, desc = "pending", fmt.Sprintf("applying… %d/%d stacks", done, total)
	}
	if err := a.gh.PostStatus(ctx, e.Repo, e.SHA, e.StatusContext, state, desc, liveURL(base, e.ID)); err != nil {
		log.Printf("apply status %s: %v", e.ID, err)
	}
}

// drive updates the GitHub surface for an execution after a state change. A
// post-merge apply (apply/<env> context) gets a commit status; the plan gate
// gets a check run (check mode) or a plan/<env> commit status (link mode).
func (a *App) drive(ctx context.Context, id, base string, terminal bool) {
	if e, err := store.GetExecution(a.db, id); err == nil && isApplyContext(e.StatusContext) {
		a.driveApply(ctx, e, base)
	} else if a.cfg.UseChecks {
		a.renderAndPatch(ctx, id, base, terminal)
	} else {
		a.reconcile(ctx, id, base)
	}
	if a.hub != nil {
		a.hub.publish("exec:"+id, "changed")
	}
}
