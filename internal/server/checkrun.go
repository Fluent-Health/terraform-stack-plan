package server

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/Fluent-Health/terraform-stack-plan/internal/config"
	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/reconcile"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// backfillFailureDetail sets Detail for failed stacks from the error tail of the
// stack's stored log excerpt. The captured log holds terraform's real error; a
// per-stack tick detail (e.g. "terraform apply failed") is only a generic
// placeholder. The log's error tail wins whenever a log was captured; stacks
// with no stored log keep their existing Detail (or empty if they had none).
func (a *App) backfillFailureDetail(execID string, g *events.Graph) {
	for i := range g.Stacks {
		s := &g.Stacks[i]
		if s.Status != events.StatusFailed {
			continue
		}
		// The captured log holds terraform's real error; a per-stack tick detail
		// (e.g. "terraform apply failed") is only a generic placeholder. Prefer the
		// log's error tail whenever a log was captured; fall back to the existing
		// detail otherwise.
		if _, excerpt, ok, _ := store.GetStackOutput(a.db, execID, s.Path, "log"); ok && excerpt != "" {
			s.Detail = errorTail(excerpt, 25)
		}
	}
}

// doneStacks counts the stacks that have finished their work for progress
// accounting, using the existing done() predicate.
func doneStacks(stacks []events.StackState) int {
	n := 0
	for _, s := range stacks {
		if done(s.Status) {
			n++
		}
	}
	return n
}

// progressTitle renders the check-run title. While running it is the live
// progress bar ("<bar> k/N · <label>"); once terminal it is an action-count
// summary ("Plan · +6 ~3 −2 · 12 stacks" / "Plan · no changes" / apply variant),
// because a frozen bar reads as stuck. kind is "plan" or "apply".
func progressTitle(prog *config.ProgressConfig, phase events.Phase, stacks []events.StackState, terminal bool, kind string) string {
	if terminal {
		kindLabel := "Plan"
		if kind == "apply" {
			kindLabel = "Apply"
		}
		return terminalSummary(kindLabel, stacks)
	}
	bar, _, label := runProgress(prog, phase, stacks, kind)
	return bar + " · " + label
}

// runProgress computes the live overall progress bar, percentage, and label for a
// running execution. It renders ONE bar across the operation's weighted phase set
// (via progress) so the bar tracks whole-operation progress, not the current
// phase; the label carries the per-phase count (e.g. "applying 1/4"), so the bar
// must NOT repeat that count. An apply still in its pre-apply re-plan pass (before
// PhaseApplying) uses that same unified bar — its warming/initializing bands fill
// and flow continuously into the applying band — but with the label "preparing
// k/N", since those per-stack ticks read as re-planning, not applying. Shared by
// the check-run title and the live page so both show the same overall bar.
func runProgress(prog *config.ProgressConfig, phase events.Phase, stacks []events.StackState, kind string) (bar string, pct int, label string) {
	total := len(stacks)
	doneCount := doneStacks(stacks)
	initCount := 0
	for _, s := range stacks {
		if s.Status == events.StatusInitialized {
			initCount++
		}
	}
	// Pre-apply re-plan pass: the bar comes from the SAME unified weighted set (so
	// the warming + initializing bands fill and flow continuously into the applying
	// band — not a separate prepared/total scale that sits at 0% then jumps). When
	// no warming/initializing preamble has been emitted yet, anchor at the first
	// band rather than the planning fallback legacyFrac would otherwise pick.
	preparing := kind == "apply" && !applyStarted(phase)
	barPhase := phase
	if preparing && barPhase == "" {
		barPhase = events.PhaseWarming
	}
	bar, label, pct = progress(prog.For(kind), barPhase, doneCount, initCount, total)
	if preparing {
		// Only the LABEL is overridden: to a reviewer these per-stack ticks read as
		// "preparing", not "initializing"/"applying".
		if total == 0 {
			return bar, pct, "preparing"
		}
		return bar, pct, fmt.Sprintf("preparing %d/%d", doneCount, total)
	}
	return bar, pct, label
}

// terminalSummary is the concluded-run title/headline tail: kind + tally + stack
// count (+ a failed suffix), or "no changes". kindLabel is "Plan" or "Apply".
func terminalSummary(kindLabel string, stacks []events.StackState) string {
	kind := kindLabel
	var failed, aborted, nochange, applied int
	for _, s := range stacks {
		switch s.Status {
		case events.StatusFailed:
			failed++
		case events.StatusAborted:
			aborted++
		case events.StatusNochange:
			nochange++
			applied++
		case events.StatusSafe:
			applied++
		}
	}
	tally := opTally(aggregateVerdict(stacks))
	var head string
	switch {
	case len(stacks) == 0:
		head = kind + " · no changes"
	case kind == "Apply":
		head = fmt.Sprintf("%s · applied %d/%d", kind, applied, len(stacks))
		if tally != "" {
			head += " · " + tally
		}
	default:
		if tally == "" {
			head = fmt.Sprintf("%s · no changes", kind)
		} else {
			head = fmt.Sprintf("%s · %s · %d stacks", kind, tally, len(stacks))
		}
	}
	if nochange > 0 && kind == "Apply" {
		head += fmt.Sprintf(" · %d no-change", nochange)
	}
	if failed > 0 {
		head += fmt.Sprintf(" · %d failed", failed)
	}
	if aborted > 0 {
		head += fmt.Sprintf(" · %d aborted", aborted)
	}
	return head
}

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
// stored). A no-op until the check run exists.
func (a *App) renderAndPatch(ctx context.Context, id, base string, terminal bool) {
	if err := store.BumpRev(a.db, id); err != nil {
		log.Printf("rev bump %s: %v", id, err)
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
	a.backfillFailureDetail(id, &g)
	// Surface pending approval gates at the top of the check run so an
	// action_required conclusion is self-explanatory (which gate, how to approve).
	targets, _ := store.TargetsFor(a.db, e.PR, e.Environment)
	upd := CheckRunUpdate{
		Title:      progressTitle(a.cfg.Progress, events.Phase(e.Phase), g.Stacks, terminal, "plan"),
		Summary:    checkSummary("plan", g.Stacks, a.liveURL(base, id), events.Phase(e.Phase)),
		Text:       gatesSection(targets) + failuresSection(g, e.LogURL, "") + e.ReportMarkdown,
		DetailsURL: a.liveURL(base, id),
	}
	// Serve-queued run (no runner data at all): name the state instead of an
	// empty progress bar / misleading "no changes" summary.
	if e.Phase == "" && len(g.Stacks) == 0 && reconcile.IsRunExecutionID(id) {
		if e.Status == "failure" {
			upd.Title = "build failed to start — use Re-run to retry"
		} else {
			upd.Title = "queued — waiting for the build to start"
		}
	}
	if terminal {
		if e.Status == "failure" && len(g.Stacks) == 0 {
			// A run that failed without runner data (start failed / watchdog) has
			// no plan snapshot to conclude from — the row status is the fact. The
			// gate-derived conclusion below can never say failure for it.
			upd.Conclusion = "failure"
		} else if snap, _, ok := loadSnapshot(a.db, id); ok {
			upd.Conclusion = conclusion(snap, applyLockVerdict{})
		}
	}
	if err := a.gh.UpdateCheckRun(ctx, e.Repo, e.CheckRunID.Int64, upd); err != nil {
		log.Printf("update check run %s: %v", id, err)
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
// Surface selection: when the execution has a check run, only the check run is
// updated — posting a redundant commit status with the same context name would
// make GitHub display two entries for the same apply. Until the check run exists
// (a brief startup window), fall back to a plain commit status.
func (a *App) driveApply(ctx context.Context, e store.Execution, base string) {
	g, err := store.LoadGraph(a.db, e.ID)
	if err != nil {
		log.Printf("apply status: load graph %s: %v", e.ID, err)
		return
	}
	a.backfillFailureDetail(e.ID, &g)
	total := len(g.Stacks)
	applied, failed := 0, 0
	for _, s := range g.Stacks {
		switch s.Status {
		case events.StatusSafe, events.StatusNochange, events.StatusMoving:
			applied++
		case events.StatusFailed:
			failed++
		}
	}
	runFailed := failed > 0 || e.Status == "failure"
	var state, desc string
	switch {
	case runFailed:
		// STACK_INIT_FAILED / STACK_APPLY_FAILED or execution-level failure flag:
		// a stack died during the apply, or the runner reported failure before
		// per-stack ticks completed. The phase (init vs apply) is carried
		// structurally in each failing stack's Detail and rendered below.
		desc = fmt.Sprintf("apply failed — %d/%d applied, %d failed; see the failing stack below — fix-forward or re-run this tier's apply", applied, total, failed)
		state = "failure"
	case total == 0 && e.Phase == "" && reconcile.IsRunExecutionID(e.ID):
		// Serve-queued run: the runner has not reported anything yet (no phase,
		// no stacks). Concluding "no stacks to apply" here would green-light an
		// apply that has not run — stay pending; the start watchdog fails it if
		// the build never materializes. Runner-created executions (non run- ids)
		// keep the legacy zero-stack success below.
		state, desc = "pending", "queued — waiting for the build to start"
	case total == 0:
		// NOTHING_TO_APPLY: no changed stacks — a no-op apply (e.g. a docs/CI-only
		// merge, or a PR whose work was a cross-state move done in the pre-phase).
		// Nothing will emit a stack-completion event to flip this to terminal, so
		// resolve it to success here rather than leaving it pending forever.
		state, desc = "success", "no stacks to apply"
	case applied == total:
		state, desc = "success", fmt.Sprintf("applied %d/%d stacks", applied, total)
	default:
		state, desc = "pending", fmt.Sprintf("applying… %d/%d stacks", applied, total)
	}
	// Persist the terminal status so the viewer's isFinished() flips (clears the
	// shimmer / live-dot / "planning" placeholder on a concluded apply). Pending
	// stays unwritten (still in-flight). Best-effort.
	if state == "success" || state == "failure" {
		if err := store.SetExecutionStatus(a.db, e.ID, state); err != nil {
			log.Printf("apply set status %s: %v", e.ID, err)
		}
	}
	if e.CheckRunID.Valid && e.CheckRunID.Int64 != 0 {
		var conclusion string
		switch state {
		case "success":
			conclusion = "success"
		case "failure":
			conclusion = "failure"
		}
		applyTerminal := state == "success" || state == "failure"
		summary := checkSummary("apply", g.Stacks, a.liveURL(base, e.ID), events.Phase(e.Phase))
		if failed > 0 {
			// Keep the next-steps guidance (fix-forward / re-run) visible in the
			// summary; the failing-stack detail renders in the Text below.
			summary += "\n\n" + desc
		}
		upd := CheckRunUpdate{
			Title:      progressTitle(a.cfg.Progress, events.Phase(e.Phase), g.Stacks, applyTerminal, "apply"),
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
// post-merge apply (apply/<env> context) updates the apply check run (falling
// back to a commit status until the check run exists); the plan gate drives its
// check run.
func (a *App) drive(ctx context.Context, id, base string, terminal bool) {
	if e, err := store.GetExecution(a.db, id); err == nil && isApplyContext(e.StatusContext) {
		a.driveApply(ctx, e, base)
	} else {
		a.renderAndPatch(ctx, id, base, terminal)
	}
	if a.hub != nil {
		a.hub.publish("exec:"+id, "changed")
	}
}
