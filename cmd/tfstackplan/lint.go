package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/runner"
)

// runLint executes static linters (e.g., tflint) over Terramate stacks.
func runLint(args []string) int {
	fs := flag.NewFlagSet("run lint", flag.ContinueOnError)
	dir := fs.String("dir", "", "terramate project root (required)")
	changed := fs.Bool("changed", true, "only lint changed stacks")
	parallel := fs.Int("parallel", 0, "parallel lint jobs (0 = terramate default)")
	base := fs.String("base", "", "git base ref for change detection")
	script := fs.String("script", "lint", "terramate script name to run")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "tfstackplan run lint: --dir is required")
		return 2
	}

	ctx := context.Background()
	tm := &runner.Terramate{Dir: *dir}
	client := runner.ClientFromEnv()
	execID := os.Getenv(runner.EnvExecution)
	if execID == "" {
		execID = newExecutionID()
	}

	var stacks []string
	var err error
	if *changed {
		stacks, err = tm.ChangedStacks(ctx, *base)
	} else {
		stacks, err = tm.List(ctx)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "tfstackplan run lint:", err)
		return 1
	}
	rawEdges, _ := tm.RunGraph(ctx)
	edges := runner.NormalizeEdges(stacks, rawEdges)

	repo, sha := os.Getenv("TFSTACKPLAN_REPO"), os.Getenv("TFSTACKPLAN_SHA")
	pr := atoiOr0(os.Getenv("TFSTACKPLAN_PR"))
	env := os.Getenv(runner.EnvEnvironment)
	initStacks := make([]events.StackState, 0, len(stacks))
	for _, s := range stacks {
		initStacks = append(initStacks, events.StackState{Path: s, Status: events.StatusPending})
	}

	_ = client.Init(ctx, events.Init{ID: execID, Repo: repo, SHA: sha, PR: pr, Environment: env, Stacks: initStacks, Edges: edges})
	_ = client.Phase(ctx, events.PhaseEvent{ID: execID, Phase: events.PhaseLinting})

	var scriptErr error
	if len(stacks) > 0 {
		os.Setenv(runner.EnvExecution, execID)
		scriptErr = tm.ScriptRun(ctx, os.Stderr, runner.ScriptRunOptions{
			Script: *script, Changed: *changed, Parallel: *parallel, Base: *base,
		})
	}

	if scriptErr != nil {
		fmt.Fprintln(os.Stderr, "tfstackplan run lint: lint failed:", scriptErr)
		_ = client.Finalize(ctx, events.Finalize{
			ID:     execID,
			Failed: true,
		})
		return 1
	}

	return 0
}
