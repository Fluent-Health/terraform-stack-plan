package server

import (
	"context"
	"log"
	"strings"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/executor"
	"github.com/Fluent-Health/terraform-stack-plan/internal/reconcile"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// runContext maps a run kind to the execution's status context: the plan gate
// context for plan runs, the apply context for post-merge applies.
func runContext(kind, environment string) string {
	if kind == reconcile.RunKindApply {
		if environment == "" {
			return "apply"
		}
		return "apply/" + environment
	}
	return statusContext(environment)
}

// startRun executes a StartRun action: materialize the queued execution + its
// check run (idempotent — feedback appears within the webhook turnaround, before
// any build machine spins up), then ask the executor to start the build. The
// returned RunStartResult feeds the shell's fixpoint loop.
func (sh *Shell) startRun(ctx context.Context, cs reconcile.ChangeSet, repo string, act reconcile.StartRun) reconcile.Signal {
	result := reconcile.RunStartResult{Kind: act.Kind, ExecutionID: act.ExecutionID}

	init := events.Init{
		ID:          act.ExecutionID,
		Repo:        repo,
		SHA:         act.SHA,
		PR:          cs.PR,
		Environment: cs.Environment,
		Context:     runContext(act.Kind, cs.Environment),
	}
	if err := store.UpsertInit(sh.app.db, init); err != nil {
		result.Err = "store queued execution: " + err.Error()
		return result
	}
	base := strings.TrimRight(sh.app.cfg.PublicBaseURL, "/")
	name := sh.app.planCheckName(cs.Environment)
	if act.Kind == reconcile.RunKindApply {
		name = init.Context
	}
	if err := sh.app.ensureCheckRun(ctx, act.ExecutionID, repo, act.SHA, name, sh.app.liveURL(base, act.ExecutionID)); err != nil {
		// The build can still run without its check run; report but continue.
		log.Printf("shell: queued check run %s: %v", act.ExecutionID, err)
	}

	if sh.app.Executor == nil {
		result.Err = "no executor configured"
		return result
	}
	ref, err := sh.app.Executor.Start(ctx, executor.RunRequest{
		Kind:        act.Kind,
		Environment: cs.Environment,
		SHA:         act.SHA,
		Branch:      act.Branch,
		ExecutionID: act.ExecutionID,
		PR:          cs.PR,
	})
	if err != nil {
		result.Err = err.Error()
		return result
	}
	result.BuildRef = ref.ID
	return result
}

// cancelRun executes a CancelRun action: mark the old execution superseded by
// the new one (the live page redirects) and best-effort cancel the old build.
func (sh *Shell) cancelRun(ctx context.Context, act reconcile.CancelRun) {
	sh.app.supersedeExecution(act.OldExecutionID, act.NewExecutionID)
	if act.OldBuildRef == "" || sh.app.Executor == nil {
		return
	}
	if err := sh.app.Executor.Cancel(ctx, executor.Ref{ID: act.OldBuildRef}); err != nil {
		log.Printf("shell: cancel superseded build %s: %v", act.OldBuildRef, err)
	}
}
