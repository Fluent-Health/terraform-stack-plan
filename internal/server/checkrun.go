package server

import (
	"context"
	"fmt"
	"log"

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
	upd := CheckRunUpdate{
		Summary:    renderProgress(g),
		Text:       failuresSection(g, e.LogURL) + e.ReportMarkdown,
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

// drive updates the GitHub surface for an execution after a state change: in
// check mode it patches the check run; in link mode it posts the commit status.
func (a *App) drive(ctx context.Context, id, base string, terminal bool) {
	if a.cfg.UseChecks {
		a.renderAndPatch(ctx, id, base, terminal)
	} else {
		a.reconcile(ctx, id, base)
	}
	if a.hub != nil {
		a.hub.publish("exec:"+id, "changed")
	}
}
