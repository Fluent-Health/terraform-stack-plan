package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/runner"
	"github.com/Fluent-Health/terraform-stack-plan/internal/statemove"
)

// runApply is the CI apply driver. It refuses to apply unless the server says
// the PR's approval gates are satisfied (fail-closed pre-check), then applies the
// changed stacks in dependency order via the terramate apply script, and revokes
// the grants afterward (best-effort). With no server configured the gate check is
// a no-op (nothing gates) and apply proceeds.
func runApply(args []string) int {
	fs := flag.NewFlagSet("run apply", flag.ContinueOnError)
	dir := fs.String("dir", "", "terramate project root (required)")
	changed := fs.Bool("changed", true, "only apply changed stacks")
	base := fs.String("base", "", "git base ref for change detection")
	script := fs.String("script", "apply", "terramate script name to run")
	logFile := fs.String("log-file", "tfstackplan.log", "per-stack log filename the terramate script writes in each stack dir; streamed live to the server (empty disables)")
	stateLock := fs.Bool("state-lock", false, "acquire a pessimistic GCS state lock around cross-state moves (fail-fast; requires ADC)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "tfstackplan run apply: --dir is required")
		return 2
	}
	ctx := context.Background()
	client := runner.ClientFromEnv()
	pr := atoiOr0(os.Getenv("TFSTACKPLAN_PR"))
	env := os.Getenv(runner.EnvEnvironment)

	// Fail-closed gate pre-check FIRST: do not touch terramate if the gate is not
	// satisfied. GateCheck no-ops when no server is configured, and errors (fail
	// closed) when a configured server is unreachable or the gate is unsatisfied.
	if _, err := client.GateCheck(ctx, events.GateCheck{PR: pr, Environment: env}); err != nil {
		fmt.Fprintln(os.Stderr, "tfstackplan run apply: refusing to apply —", err)
		return 1
	}

	// Fail-closed cross-state move pre-phase: execute any pending
	// `_tfsp_xmove.*.hcl` manifests before the apply runs. No-op when none are
	// present. A failure here aborts the apply (the moves must land cleanly,
	// otherwise the apply would plan against a stale/half-moved state).
	var stateLocker statemove.Locker
	if *stateLock {
		l, err := gcsLockerFromADC(ctx)
		if err != nil {
			fmt.Fprintln(os.Stderr, "tfstackplan run apply: --state-lock:", err)
			return 1
		}
		stateLocker = l
	}
	if err := applyPendingMoves(ctx, *dir, true, stateLocker, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "tfstackplan run apply: cross-state move failed:", err)
		return 1
	}

	tm := &runner.Terramate{Dir: *dir}
	execID := os.Getenv(runner.EnvExecution)
	if execID == "" {
		execID = fmt.Sprintf("apply-%d", time.Now().UnixNano())
	}

	var stacks []string
	var err error
	if *changed {
		stacks, err = tm.ChangedStacks(ctx, *base)
	} else {
		stacks, err = tm.List(ctx)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "tfstackplan run apply:", err)
		return 1
	}
	edges, _ := tm.RunGraph(ctx)

	repo, sha := os.Getenv("TFSTACKPLAN_REPO"), os.Getenv("TFSTACKPLAN_SHA")
	initStacks := make([]events.StackState, 0, len(stacks))
	for _, s := range stacks {
		initStacks = append(initStacks, events.StackState{Path: s, Status: events.StatusPending})
	}
	applyCtx := "apply"
	if env != "" {
		applyCtx = "apply/" + env
	}
	_ = client.Init(ctx, events.Init{ID: execID, Repo: repo, SHA: sha, PR: pr, Environment: env, Context: applyCtx, Stacks: initStacks, Edges: edges})
	_ = client.Phase(ctx, events.PhaseEvent{ID: execID, Phase: events.PhaseApplying})

	var applyErr error
	if len(stacks) > 0 {
		os.Setenv(runner.EnvExecution, execID)
		var stop func()
		if client.Enabled() && *logFile != "" {
			stop = runner.NewLogPump(client, *dir, *logFile, execID).Start(stacks)
		}
		// No --parallel: terramate applies in dependency order, serially.
		applyErr = tm.ScriptRun(ctx, os.Stderr, runner.ScriptRunOptions{Script: *script, Changed: *changed, Base: *base})
		if stop != nil {
			stop()
		}
	}

	// Best-effort post-apply cleanup: revoke the PR's grants.
	_ = client.GateRevoke(ctx, events.GateRevoke{PR: pr, Environment: env})

	if applyErr != nil {
		fmt.Fprintln(os.Stderr, "tfstackplan run apply: apply failed:", applyErr)
		return 1
	}
	return 0
}
