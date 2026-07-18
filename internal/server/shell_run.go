package server

import (
	"context"
	"log"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/execution"
	"github.com/Fluent-Health/terraform-stack-plan/internal/executor"
	"github.com/Fluent-Health/terraform-stack-plan/internal/reconcile"
)

// execInitFromEvents maps the wire-level events.Init into the execution
// aggregate's State, so both handleInit and materializeRun can drive
// ReportInit through the same HandleExec path. Stack status defaults to
// StatusPending when the caller omits it (mirrors the old UpsertInit).
// Status is seeded "in_progress": the legacy UpsertInit got this for free from
// the executions.status column DEFAULT (its INSERT never named the column);
// ProjectExecutionRow's INSERT always names it, so Started must carry it
// explicitly or a (re-)init would project status="" instead.
func execInitFromEvents(in events.Init) execution.State {
	st := execution.State{
		ID: in.ID, Repo: in.Repo, SHA: in.SHA, PR: in.PR,
		Environment: in.Environment, Context: in.Context, LogURL: in.LogURL,
		Status: "in_progress",
	}
	for _, s := range in.Stacks {
		status := s.Status
		if status == "" {
			status = events.StatusPending
		}
		st.Stacks = append(st.Stacks, execution.Stack{
			Path: s.Path, Project: s.Project, RunStatus: status, Counts: s.Counts,
		})
	}
	for _, e := range in.Edges {
		st.Edges = append(st.Edges, execution.Edge{From: e.From, To: e.To})
	}
	return st
}

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

// materializeRun creates the queued execution row + its check run for a run
// (idempotent — feedback appears within the webhook turnaround, before any build
// machine spins up). Shared by startRun (serve-launched) and adoptRun (a build
// serve did not launch). Returns an error only when the execution row can't be
// written; check-run creation is best-effort (the build can still run without it).
func (sh *Shell) materializeRun(ctx context.Context, cs reconcile.ChangeSet, repo, execID, sha, kind string) error {
	init := events.Init{
		ID:          execID,
		Repo:        repo,
		SHA:         sha,
		PR:          cs.PR,
		Environment: cs.Environment,
		Context:     runContext(kind, cs.Environment),
	}
	if err := sh.HandleExec(ctx, execID, execution.ReportInit{Exec: execInitFromEvents(init)}); err != nil {
		return err
	}
	name := sh.app.planCheckName(cs.Environment)
	if kind == reconcile.RunKindApply {
		name = init.Context
	}
	if err := sh.app.ensureCheckRun(ctx, execID, repo, sha, name, sh.app.uiURL(cs.PR, execID)); err != nil {
		log.Printf("shell: check run %s: %v", execID, err)
	}
	return nil
}

// startRun executes a StartRun action: materialize the queued execution + check
// run, then ask the executor to start the build. The returned RunStartResult
// feeds the shell's fixpoint loop.
func (sh *Shell) startRun(ctx context.Context, cs reconcile.ChangeSet, repo string, act reconcile.StartRun) reconcile.Signal {
	result := reconcile.RunStartResult{Kind: act.Kind, ExecutionID: act.ExecutionID}
	if err := sh.materializeRun(ctx, cs, repo, act.ExecutionID, act.SHA, act.Kind); err != nil {
		result.Err = "store queued execution: " + err.Error()
		return result
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

// adoptRun executes an AdoptRun action: materialize the execution row + check run
// for a run bound to a build serve did NOT launch. Unlike startRun it never calls
// the executor — the build already exists, and its ref rode in on the RunAdopted
// event (folded into run state). The watchdog then tracks that build.
func (sh *Shell) adoptRun(ctx context.Context, cs reconcile.ChangeSet, repo string, act reconcile.AdoptRun) {
	if err := sh.materializeRun(ctx, cs, repo, act.ExecutionID, act.SHA, act.Kind); err != nil {
		log.Printf("shell: adopt run %s: store execution: %v", act.ExecutionID, err)
	}
}

// cancelRun executes a CancelRun action: mark the old execution superseded by
// the new one (the live page redirects) and best-effort cancel the old build.
func (sh *Shell) cancelRun(ctx context.Context, act reconcile.CancelRun) {
	sh.app.supersedeExecution(ctx, act.OldExecutionID, act.NewExecutionID)
	if act.OldBuildRef == "" || sh.app.Executor == nil {
		return
	}
	if err := sh.app.Executor.Cancel(ctx, executor.Ref{ID: act.OldBuildRef}); err != nil {
		log.Printf("shell: cancel superseded build %s: %v", act.OldBuildRef, err)
	}
}
