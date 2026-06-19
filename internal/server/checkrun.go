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
func (a *App) ensureCheckRun(ctx context.Context, id, repo, sha, name, detailsURL string) error {
	e, err := store.GetExecution(a.db, id)
	if err != nil {
		return err
	}
	if e.CheckRunID.Valid && e.CheckRunID.Int64 != 0 {
		return nil
	}
	crID, err := a.gh.CreateCheckRun(ctx, repo, sha, name, detailsURL)
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
		Title:      "Terraform plan",
		Summary:    checkSummary("plan", e.Environment, events.Phase(e.Phase), g.Stacks, a.liveURL(base, id)),
		Text:       gatesSection(targets) + failuresSection(g, e.LogURL, "") + e.ReportMarkdown,
		DetailsURL: a.liveURL(base, id),
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
	if err := a.gh.PostStatus(ctx, e.Repo, e.SHA, statusContext(e.Environment), st.state, st.desc, a.liveURL(base, id)); err != nil {
		log.Printf("reconcile status %s: %v", id, err)
	}
}

// isApplyContext reports whether a status context is a post-merge apply (e.g.
// "apply/nonprod") rather than the plan gate.
func isApplyContext(ctx string) bool {
	return ctx == "apply" || strings.HasPrefix(ctx, "apply/")
}

// driveApply updates the GitHub surface for a post-merge apply execution.
// State derives from the stacks: any failed ⇒ failure; all terminal (or no
// stacks at all — a no-op apply) ⇒ success; otherwise pending. Best-effort.
//
// Surface selection: when UseChecks is on and the execution has a check run,
// only the check run is updated — posting a redundant commit status with the
// same context name would make GitHub display two entries for the same apply.
// When there is no check run (UseChecks off or check run not yet created), fall
// back to a plain commit status.
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
		// STACK_INIT_FAILED / STACK_APPLY_FAILED: a stack died during the apply.
		// The phase (init vs apply) is carried structurally in each failing
		// stack's Detail and rendered (with a per-stack log deep-link) below.
		desc = fmt.Sprintf("apply failed — %d/%d applied, %d failed; see the failing stack below — fix-forward or re-run this tier's apply", done, total, failed)
		state = "failure"
	case total == 0:
		// NOTHING_TO_APPLY: no changed stacks — a no-op apply (e.g. a docs/CI-only
		// merge, or a PR whose work was a cross-state move done in the pre-phase).
		// Nothing will emit a stack-completion event to flip this to terminal, so
		// resolve it to success here rather than leaving it pending forever.
		state, desc = "success", "no stacks to apply"
	case done == total:
		state, desc = "success", fmt.Sprintf("applied %d/%d stacks", done, total)
	default:
		state, desc = "pending", fmt.Sprintf("applying… %d/%d stacks", done, total)
	}
	if a.cfg.UseChecks && e.CheckRunID.Valid && e.CheckRunID.Int64 != 0 {
		var conclusion string
		switch state {
		case "success":
			conclusion = "success"
		case "failure":
			conclusion = "failure"
		}
		summary := checkSummary("apply", e.Environment, events.Phase(e.Phase), g.Stacks, a.liveURL(base, e.ID))
		if failed > 0 {
			// Keep the next-steps guidance (fix-forward / re-run) visible in the
			// summary; the failing-stack detail renders in the Text below.
			summary += "\n\n" + desc
		}
		upd := CheckRunUpdate{
			Title:      "Terraform apply",
			Summary:    summary,
			DetailsURL: a.liveURL(base, e.ID),
			Conclusion: conclusion,
		}
		// On failure, attribute the failing stack(s) + phase in the body with a
		// deep-link to each stack's own streamed log.
		if failed > 0 {
			upd.Text = failuresSection(g, e.LogURL, strings.TrimRight(base, "/")+"/logs/"+e.ID)
		}
		if err := a.gh.UpdateCheckRun(ctx, e.Repo, e.CheckRunID.Int64, upd); err != nil {
			log.Printf("apply check run %s: %v", e.ID, err)
		}
		return
	}
	if err := a.gh.PostStatus(ctx, e.Repo, e.SHA, e.StatusContext, state, desc, a.liveURL(base, e.ID)); err != nil {
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
