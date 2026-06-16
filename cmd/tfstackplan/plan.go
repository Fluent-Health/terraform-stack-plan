package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/Fluent-Health/terraform-stack-plan/internal/config"
	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/runner"
)

// runPlan is the CI plan driver: detect changed stacks, register the execution,
// run the terramate plan script, then render + classify the produced plans and
// finalize to the server. Server reporting is best-effort; the report is always
// rendered (and printed) so a local run is useful offline.
func runPlan(args []string) int {
	fs := flag.NewFlagSet("run plan", flag.ContinueOnError)
	dir := fs.String("dir", "", "terramate project root (required)")
	changed := fs.Bool("changed", true, "only plan changed stacks")
	parallel := fs.Int("parallel", 0, "parallel plan jobs (0 = terramate default)")
	base := fs.String("base", "", "git base ref for change detection")
	script := fs.String("script", "plan", "terramate script name to run")
	logFile := fs.String("log-file", "tfstackplan.log", "per-stack log filename the terramate script writes in each stack dir; streamed live to the server (empty disables)")
	cfgPath := fs.String("config", "", "HCL config (default: auto-discover .tfstackplan.hcl under --dir)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "tfstackplan run plan: --dir is required")
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
		fmt.Fprintln(os.Stderr, "tfstackplan run plan:", err)
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
	_ = client.Phase(ctx, events.PhaseEvent{ID: execID, Phase: events.PhasePlanning})

	var scriptErr error
	if len(stacks) > 0 {
		os.Setenv(runner.EnvExecution, execID)
		var stop func()
		if client.Enabled() && *logFile != "" {
			stop = runner.NewLogPump(client, *dir, *logFile, execID).Start(stacks)
		}
		scriptErr = tm.ScriptRun(ctx, os.Stderr, runner.ScriptRunOptions{
			Script: *script, Changed: *changed, Parallel: *parallel, Base: *base,
		})
		if stop != nil {
			stop()
		}
	}

	// Render + classify the produced plans (shared with the apply-time classify
	// pass via renderClassification).
	res, rerr := renderClassification(*dir, stacks, *cfgPath)
	if rerr != nil {
		fmt.Fprintln(os.Stderr, "tfstackplan run plan:", rerr)
		return 1
	}
	fmt.Print(res.Report)

	_ = client.Finalize(ctx, events.Finalize{
		ID: execID, ReportMarkdown: res.Report, StackReports: res.StackReports, Gates: res.Gates, Moving: res.Moving, Failed: scriptErr != nil,
		Categories: res.Categories,
	})

	if scriptErr != nil {
		fmt.Fprintln(os.Stderr, "tfstackplan run plan: plan failed:", scriptErr)
		return 1
	}
	return 0
}

// gatingClasses loads the config and returns the set of class names that have a
// `class` binding (those are the approval gates).
func gatingClasses(cfgPath, dir string) map[string]bool {
	p := cfgPath
	if p == "" {
		if d, ok := config.Discover(dir); ok {
			p = d
		}
	}
	out := map[string]bool{}
	if p == "" {
		return out
	}
	cfg, err := config.Load(p)
	if err != nil {
		return out
	}
	for _, c := range cfg.Classes {
		out[c.Name] = true
	}
	return out
}

func atoiOr0(s string) int { n, _ := strconv.Atoi(s); return n }

// newExecutionID returns an execution id for an un-orchestrated run.
func newExecutionID() string {
	return fmt.Sprintf("plan-%d", time.Now().UnixNano())
}

// gatherPlans collects each stack's tfplan.json (written by the terramate plan
// script in the stack dir, i.e. <root>/<stack>/tfplan.json) into a fresh temp
// plans-dir laid out as <plansDir>/<stack>/tfplan.json — the shape the render
// pipeline (--plans-dir) expects. Stacks without a tfplan.json (e.g. a plan
// failure) are skipped. The caller removes plansDir when done.
func gatherPlans(root string, stacks []string) (string, error) {
	plansDir, err := os.MkdirTemp("", "tfstackplan-plans-")
	if err != nil {
		return "", err
	}
	for _, s := range stacks {
		src := filepath.Join(root, filepath.FromSlash(s), "tfplan.json")
		data, err := os.ReadFile(src)
		if err != nil {
			continue // no plan for this stack (skipped/failed) — not fatal
		}
		dst := filepath.Join(plansDir, filepath.FromSlash(s))
		if err := os.MkdirAll(dst, 0o755); err != nil {
			os.RemoveAll(plansDir)
			return "", err
		}
		if err := os.WriteFile(filepath.Join(dst, "tfplan.json"), data, 0o644); err != nil {
			os.RemoveAll(plansDir)
			return "", err
		}
	}
	return plansDir, nil
}
