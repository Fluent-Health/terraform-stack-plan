package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/runner"
)

// runRegister lists the stack set and registers it with the server up front
// (one Init, all stacks pending) so phase events emitted before `run plan`/`run
// apply` (warming/initializing) already show the real stack count instead of 0.
// It is a no-op offline and never fails the build except on a flag-parse error.
func runRegister(args []string) int {
	fs := flag.NewFlagSet("run register", flag.ContinueOnError)
	dir := fs.String("dir", ".", "terramate root directory")
	changed := fs.Bool("changed", false, "only changed stacks (terramate --changed)")
	base := fs.String("base", "", "git base ref for --changed")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	client := runner.ClientFromEnv()
	execID := os.Getenv(runner.EnvExecution)
	if !client.Enabled() || execID == "" {
		return 0 // offline / un-orchestrated: nothing to register
	}

	ctx := context.Background()
	tm := newTerramate(*dir)
	var stacks []string
	var err error
	if *changed {
		stacks, err = tm.ChangedStacks(ctx, *base)
	} else {
		stacks, err = tm.List(ctx)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "tfstackplan run register:", err)
		return 1
	}
	edges, _ := tm.RunGraph(ctx) // best-effort; edges only enrich the graph

	repo, sha := os.Getenv("TFSTACKPLAN_REPO"), os.Getenv("TFSTACKPLAN_SHA")
	pr := atoiOr0(os.Getenv("TFSTACKPLAN_PR"))
	env := os.Getenv(runner.EnvEnvironment)
	initStacks := make([]events.StackState, 0, len(stacks))
	for _, s := range stacks {
		initStacks = append(initStacks, events.StackState{Path: s, Status: events.StatusPending})
	}
	_ = client.Init(ctx, events.Init{ID: execID, Repo: repo, SHA: sha, PR: pr, Environment: env, Stacks: initStacks, Edges: edges})
	return 0
}
