package main

import (
	"context"
	"os"
	"path/filepath"

	"github.com/Fluent-Health/terraform-stack-plan/internal/config"
	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/runner"
)

// classifyForGate runs the terramate plan script over the changed stacks, renders
// the classification sidecar, and returns the gate targets, per-stack categories,
// moving stacks, and the rendered report. It is the shared classify pass used by
// `run plan` and (at apply time) by `run apply` to (re-)establish the gate's
// classification + grant requests, keyed to the same (pr, environment).
//
// dir is the terramate project root; base is the git base ref for change
// detection (empty = terramate default); cfgPath is an explicit config path
// (empty = auto-discover .tfstackplan.hcl under dir).
//
// It is a package var so cmd-level apply tests can stub the pass without a real
// terramate/terraform on PATH. The stubbable seam (classifyForGateFn) returns the
// gate-relevant outputs; classifyResult additionally carries the per-stack reports
// that run plan finalizes (apply ignores them).
var classifyForGateFn = func(ctx context.Context, dir string, stacks []string, base string, changed bool, cfgPath string) (
	[]events.GateTarget, map[string][]events.Category, []string, string, error) {
	r, err := classifyForGate(ctx, dir, stacks, base, changed, cfgPath)
	return r.Gates, r.Categories, r.Moving, r.Report, err
}

// classifyResult is the full output of a classify pass.
type classifyResult struct {
	Gates        []events.GateTarget
	Categories   map[string][]events.Category
	Moving       []string
	Report       string
	StackReports map[string]string
}

// classifyForGate runs the terramate plan script over the given stacks (so each
// writes a fresh tfplan.json) and renders + classifies the result. The caller
// passes the already-resolved stack set plus the change-detection flags it used
// to compute it (changed/base), which select the script-run scope. At apply time
// we re-plan rather than reuse a saved plan: a stale/locked plan can't be
// trusted, mirroring the plan script's own -lock semantics.
func classifyForGate(ctx context.Context, dir string, stacks []string, base string, changed bool, cfgPath string) (classifyResult, error) {
	tm := newTerramate(dir)
	if len(stacks) > 0 {
		if rerr := tm.ScriptRun(ctx, os.Stderr, runner.ScriptRunOptions{Script: "plan", Changed: changed, Base: base}); rerr != nil {
			return classifyResult{}, rerr
		}
	}
	return renderClassification(dir, stacks, cfgPath)
}

// renderClassification gathers the per-stack tfplan.json (already written by the
// plan script), renders the report, classifies, and reads the gate/category/move
// outputs from the sidecar. It is the render+classify core shared by run plan
// (which runs the plan script itself, with the live log pump) and classifyForGate
// (the apply-time pass). cfgPath empty ⇒ auto-discover .tfstackplan.hcl under dir.
func renderClassification(dir string, stacks []string, cfgPath string) (classifyResult, error) {
	plansDir, gerr := gatherPlans(dir, stacks)
	if gerr != nil {
		return classifyResult{}, gerr
	}
	defer os.RemoveAll(plansDir)

	resolvedCfg := cfgPath
	if resolvedCfg == "" {
		if p, ok := config.Discover(dir); ok {
			resolvedCfg = p
		}
	}

	sidecar := filepath.Join(plansDir, "_classes.json")
	o := opts{
		plansDir:  plansDir,
		title:     "Terraform plan",
		marker:    "tfstackplan",
		config:    resolvedCfg,
		maxBytes:  defaultMaxBytes,
		output:    "-",
		details:   "closed",
		repoRoot:  dir,
		classJSON: sidecar,
	}
	report, stackReports, _, rerr := run(o)
	if rerr != nil {
		return classifyResult{}, rerr
	}

	res := classifyResult{
		Gates:        []events.GateTarget{},
		Categories:   map[string][]events.Category{},
		Moving:       []string{},
		Report:       report,
		StackReports: stackReports,
	}
	if data, e := os.ReadFile(sidecar); e == nil {
		res.Gates, res.Moving, _ = gatesFromSidecar(data, gatingClasses(resolvedCfg, dir))
		res.Categories, _ = categoriesFromSidecar(data)
	}
	return res, nil
}
