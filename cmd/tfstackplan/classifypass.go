package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Fluent-Health/terraform-stack-plan/internal/config"
	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/plan"
	"github.com/Fluent-Health/terraform-stack-plan/internal/runner"
	"github.com/Fluent-Health/terraform-stack-plan/internal/statemove"
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
var classifyForGateFn = func(ctx context.Context, dir string, stacks []string, base string, changed bool, cfgPath string, parallel int) (
	[]events.GateTarget, map[string][]events.Category, map[string]events.Counts, []string, string, error) {
	r, err := classifyForGate(ctx, dir, stacks, base, changed, cfgPath, parallel)
	return r.Gates, r.Categories, r.Counts, r.Moving, r.ReportNoTable, err
}

// classifyResult is the full output of a classify pass.
type classifyResult struct {
	Gates         []events.GateTarget
	Categories    map[string][]events.Category
	Moving        []string
	Counts        map[string]events.Counts
	Report        string
	ReportNoTable string
	StackReports  map[string]string
}

// classifyForGate runs the terramate plan script over the given stacks (so each
// writes a fresh tfplan.json) and renders + classifies the result. The caller
// passes the already-resolved stack set plus the change-detection flags it used
// to compute it (changed/base), which select the script-run scope. At apply time
// we re-plan rather than reuse a saved plan: a stale/locked plan can't be
// trusted, mirroring the plan script's own -lock semantics. parallel mirrors the
// apply's --parallel so the re-plan runs N-wide instead of one stack at a time.
func classifyForGate(ctx context.Context, dir string, stacks []string, base string, changed bool, cfgPath string, parallel int) (classifyResult, error) {
	tm := newTerramate(dir)
	if len(stacks) > 0 {
		if rerr := tm.ScriptRun(ctx, os.Stderr, runner.ScriptRunOptions{Script: "plan", Changed: changed, Base: base, Parallel: parallel}); rerr != nil {
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

	// Reconcile pending cross-state moves: discover the committed move shims +
	// xmove manifests under dir and feed their two-sided targets to classification
	// (--state-moves), so a move-out (planned destroy) and a move-in (planned
	// create) classify as 🚚 (non-iam) instead of "iam + destructive". This mirrors
	// the apply pre-phase (applyPendingMoves), which executes every pending move
	// regardless of PR — so we collect all (want == "") for a consistent verdict.
	// Fail-closed: a malformed manifest errors the pass rather than silently
	// classifying a relocation as a real IAM change.
	if movesPath, merr := writeStateMovesManifest(dir, plansDir); merr != nil {
		return classifyResult{}, merr
	} else if movesPath != "" {
		o.stateMoves = movesPath
	}

	report, reportNoTable, stackReports, _, rerr := run(o)
	if rerr != nil {
		return classifyResult{}, rerr
	}

	res := classifyResult{
		Gates:         []events.GateTarget{},
		Categories:    map[string][]events.Category{},
		Moving:        []string{},
		Counts:        map[string]events.Counts{},
		Report:        report,
		ReportNoTable: reportNoTable,
		StackReports:  stackReports,
	}
	if data, e := os.ReadFile(sidecar); e == nil {
		res.Gates, res.Moving, _ = gatesFromSidecar(data, gatingClasses(resolvedCfg, dir))
		res.Categories, _ = categoriesFromSidecar(data)
		res.Counts, _ = countsFromSidecar(data)
	}
	return res, nil
}

// writeStateMovesManifest discovers all pending cross-state moves under dir and
// writes their two-sided --state-moves JSON into outDir, returning the file path.
// It returns ("", nil) when there are no pending moves (the common case), so the
// caller leaves --state-moves unset. The file lives under the caller's plansDir,
// which is removed when the classify pass finishes.
func writeStateMovesManifest(dir, outDir string) (string, error) {
	manifest, err := collectStateMoves(dir, "")
	if err != nil {
		return "", err
	}
	if len(manifest) == 0 {
		return "", nil
	}

	// Validate xmove From addresses against plans
	if err := validateXMoveManifest(dir, outDir); err != nil {
		return "", err
	}

	b, err := json.Marshal(manifest)
	if err != nil {
		return "", err
	}
	path := filepath.Join(outDir, "_state_moves.json")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func validateXMoveManifest(dir, outDir string) error {
	xmoves, err := statemove.DiscoverXMoves(dir)
	if err != nil {
		return err
	}

	var hasErrors bool

	for _, fx := range xmoves {
		resolvedSource := resolveSourceStack(dir, fx.DestStack, fx.XMove.SourceStack)

		// Build Source AddressSet from prior_state.
		// prior_state holds the unprocessed addresses — the same ones apply-time
		// validation will see in the live state. ResourceChanges addresses are
		// post-moved{}-processing and diverge from live state when module-level
		// moved{} blocks are present, so we do not fall back to them.
		// A plan without prior_state means the source stack is new — there is
		// nothing to move out, and we hard-error rather than silently producing
		// a false-safe classification.
		srcPlanPath := filepath.Join(outDir, filepath.FromSlash(resolvedSource), "tfplan.json")
		srcPlanBytes, rerr := os.ReadFile(srcPlanPath)
		if rerr != nil {
			// xmove/source-not-planned: the source stack is not in the changed set
			// for this PR. Touch the source stack to include it, or remove this
			// manifest if the cross-state move has already been applied.
			fmt.Fprintf(os.Stderr, "❌ xmove %s: xmove/source-not-planned — source stack %q is not in the changed set; touch it so Terramate plans it, or remove this manifest if the move is already complete\n", fx.Key, resolvedSource)
			hasErrors = true
			continue
		}
		priorAddrs := statemove.PriorStateAddrs(srcPlanBytes)
		if priorAddrs == nil {
			fmt.Fprintf(os.Stderr, "❌ xmove %s: source stack %q has no prior_state (stack is new — nothing to move)\n", fx.Key, resolvedSource)
			hasErrors = true
			continue
		}
		srcAddrs := priorAddrs

		// Build Destination AddressSet
		dstAddrs := statemove.AddressSet{}
		dstPlanPath := filepath.Join(outDir, filepath.FromSlash(fx.DestStack), "tfplan.json")
		if dstPlanBytes, err := os.ReadFile(dstPlanPath); err == nil {
			if rs, err := plan.Parse(fx.DestStack, dstPlanBytes); err == nil {
				for _, c := range rs.Changes {
					dstAddrs[c.Address] = c.ProviderName
					if c.PreviousAddress != "" {
						dstAddrs[c.PreviousAddress] = c.ProviderName
					}
				}
			}
		}

		// Discover Destination Providers
		destStackDir := filepath.Join(dir, filepath.FromSlash(fx.DestStack))
		destProviders := statemove.DiscoverDestProviders(destStackDir)

		// Run ValidateMovePlan
		diags := statemove.ValidateMovePlan(srcAddrs, dstAddrs, destProviders, fx.XMove, false)

		// Process and Print Diagnostics
		for _, diag := range diags {
			if diag.Severity == statemove.SeverityError {
				fmt.Fprintf(os.Stderr, "❌ xmove %s: %s\n", fx.Key, diag.Message)
				hasErrors = true
			} else {
				fmt.Fprintf(os.Stderr, "⚠️  xmove %s: %s\n", fx.Key, diag.Message)
			}
		}
	}

	if hasErrors {
		return fmt.Errorf("xmove manifest validation failed")
	}
	return nil
}
