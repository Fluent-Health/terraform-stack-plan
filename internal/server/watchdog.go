package server

import (
	"context"
	"log"
	"time"

	"github.com/Fluent-Health/terraform-stack-plan/internal/executor"
	"github.com/Fluent-Health/terraform-stack-plan/internal/reconcile"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// RunWatchdogLoop backstops serve-initiated runs: a run whose build never
// produces a reporting runner would otherwise hang its check forever. Every
// interval it probes executions that are still queued (no runner activity)
// past timeout and fails the ones whose build is gone, failed, or finished
// silent. No-op unless run triggering is armed.
func (a *App) RunWatchdogLoop(ctx context.Context, interval, timeout time.Duration) {
	if !a.runTriggerArmed() {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.watchRunsOnce(ctx, timeout)
		}
	}
}

// watchRunsOnce probes one round of stuck queued executions.
func (a *App) watchRunsOnce(ctx context.Context, timeout time.Duration) {
	stuck, err := store.StuckPendingExecutions(a.db, a.cfg.Environment, a.now().Add(-timeout))
	if err != nil {
		log.Printf("watchdog: list stuck executions: %v", err)
		return
	}
	for _, e := range stuck {
		kind := reconcile.RunKindPlan
		if isApplyContext(e.StatusContext) {
			kind = reconcile.RunKindApply
		}
		world, err := a.shell.gather(e.PR, e.Environment)
		if err != nil {
			log.Printf("watchdog: gather pr=%d env=%s: %v", e.PR, e.Environment, err)
			continue
		}
		r, ok := world.Prior.Runs[kind]
		if !ok || r.ExecutionID != e.ID || !r.Live() {
			// Not a serve-initiated run (a runner-created execution), or the run
			// already moved on (superseded / failed) — not the watchdog's business.
			continue
		}
		reason := ""
		if r.BuildRef == "" {
			// StartRun never completed (crash between persist and feedback).
			reason = "build start never completed"
		} else {
			phase, perr := a.Executor.Probe(ctx, executor.Ref{ID: r.BuildRef})
			if perr != nil {
				log.Printf("watchdog: probe %s: %v", r.BuildRef, perr)
				continue
			}
			switch phase {
			case executor.PhaseQueued, executor.PhaseWorking:
				continue // still provisioning/running — the check title says queued
			case executor.PhaseFailed:
				reason = "build failed before the runner reported"
			case executor.PhaseDone:
				reason = "build finished without the runner ever reporting"
			case executor.PhaseNotFound:
				reason = "build not found at the executor"
			}
		}
		if err := a.shell.Handle(ctx, e.PR, e.Environment, e.Repo, reconcile.RunStartResult{
			Kind: kind, ExecutionID: e.ID, Err: reason,
		}); err != nil {
			log.Printf("watchdog: fail run %s: %v", e.ID, err)
		}
	}
}
