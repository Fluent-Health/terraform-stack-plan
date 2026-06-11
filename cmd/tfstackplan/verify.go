package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/runner"
)

// runVerify is the CI verify driver: it runs the terramate verify script across
// the changed stacks and reports a verify/<env> check run with its own live page.
// Unlike apply it does NOT gate (verification is read-only post-apply validation).
// Server reporting is best-effort; per-stack output streams via the log pump.
func runVerify(args []string) int {
	fs := flag.NewFlagSet("run verify", flag.ContinueOnError)
	dir := fs.String("dir", "", "terramate project root (required)")
	changed := fs.Bool("changed", true, "only verify changed stacks")
	base := fs.String("base", "", "git base ref for change detection")
	script := fs.String("script", "verify", "terramate script name to run")
	logFile := fs.String("log-file", "tfstackplan.log", "per-stack log filename the terramate script writes in each stack dir; streamed live to the server (empty disables)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "tfstackplan run verify: --dir is required")
		return 2
	}
	ctx := context.Background()
	client := runner.ClientFromEnv()
	tm := &runner.Terramate{Dir: *dir}
	pr := atoiOr0(os.Getenv("TFSTACKPLAN_PR"))
	env := os.Getenv(runner.EnvEnvironment)
	execID := os.Getenv(runner.EnvExecution)
	if execID == "" {
		execID = fmt.Sprintf("verify-%d", time.Now().UnixNano())
	}

	var stacks []string
	var err error
	if *changed {
		stacks, err = tm.ChangedStacks(ctx, *base)
	} else {
		stacks, err = tm.List(ctx)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "tfstackplan run verify:", err)
		return 1
	}
	edges, _ := tm.RunGraph(ctx)

	repo, sha := os.Getenv("TFSTACKPLAN_REPO"), os.Getenv("TFSTACKPLAN_SHA")
	initStacks := make([]events.StackState, 0, len(stacks))
	for _, s := range stacks {
		initStacks = append(initStacks, events.StackState{Path: s, Status: events.StatusPending})
	}
	verifyCtx := "verify"
	if env != "" {
		verifyCtx = "verify/" + env
	}
	_ = client.Init(ctx, events.Init{ID: execID, Repo: repo, SHA: sha, PR: pr, Environment: env, Context: verifyCtx, Stacks: initStacks, Edges: edges})
	_ = client.Phase(ctx, events.PhaseEvent{ID: execID, Phase: events.PhaseVerifying})

	var verifyErr error
	if len(stacks) > 0 {
		os.Setenv(runner.EnvExecution, execID)
		var stop func()
		if client.Enabled() && *logFile != "" {
			stop = runner.NewLogPump(client, *dir, *logFile, execID).Start(stacks)
		}
		verifyErr = tm.ScriptRun(ctx, os.Stderr, runner.ScriptRunOptions{Script: *script, Changed: *changed, Base: *base})
		if stop != nil {
			stop()
		}
	}

	// Finalize concludes the verify/<env> check run via the existing verdict logic:
	// Failed → stacks marked failed → "failure"; otherwise the non-empty report hits
	// the "success" branch (and post-apply PR gates remain ACTIVE → also success).
	report := fmt.Sprintf("✅ Verification passed (%d stacks).", len(stacks))
	if verifyErr != nil {
		report = "❌ Verification failed."
	}
	_ = client.Finalize(ctx, events.Finalize{ID: execID, ReportMarkdown: report, Failed: verifyErr != nil})

	if verifyErr != nil {
		fmt.Fprintln(os.Stderr, "tfstackplan run verify: verify failed:", verifyErr)
		return 1
	}
	return 0
}
