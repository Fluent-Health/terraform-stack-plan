package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/runner"
	"github.com/Fluent-Health/terraform-stack-plan/internal/statemove"
)

// tmRunner is the subset of the terramate driver the apply orchestrator needs.
// It is an interface so cmd-level tests can inject a fake without a real
// terramate/terraform on PATH (the e2e fixtures still exercise the real driver).
type tmRunner interface {
	ChangedStacks(ctx context.Context, base string) ([]string, error)
	List(ctx context.Context) ([]string, error)
	RunGraph(ctx context.Context) ([]events.Edge, error)
	ScriptRun(ctx context.Context, w io.Writer, o runner.ScriptRunOptions) error
}

// newTerramate builds the terramate driver for a project root. A package var so
// tests can substitute a fake.
var newTerramate = func(dir string) tmRunner { return &runner.Terramate{Dir: dir} }

// applyMovesFn is the cross-state move pre-phase. A package var so tests can
// simulate a move failure without a real terraform/state.
var applyMovesFn func(ctx context.Context, dir string, execute bool, locker statemove.Locker, w io.Writer, sink func(stack, line string)) error = applyPendingMoves

// runApply is the CI apply driver. It is self-sufficient (re-classifies and
// re-requests grants at apply time, so a merged PR recovers even if serve state
// was wiped) and self-explaining (every exit path emits a terminal Finalize, so
// the apply check run always concludes with a cause + per-stack attribution).
//
// Order:
//  1. parse flags; compute the changed stacks (-B merge^).
//  2. zero stacks → Init(empty) + Finalize(success) + exit 0 (never touch the gate).
//  3. Init(apply ctx) + Phase(applying).
//  4. classify pass → Finalize{Gates,…} (re-classify + request grants), keyed to (pr,env).
//     Under --impersonate-requester a classify FAILURE is fail-closed (abort): we
//     must not run an elevated-intent apply under the ambient identity (→ 403).
//  5. GateCheck (fail-closed) → on reject, classified next-steps to stderr + exit 1.
//  6. impersonate the leased requester SA (optional).
//  7. cross-state move pre-phase → on failure, Finalize{Failed} + exit 1.
//  8. terramate apply script (live log pump).
//  9. Finalize{Failed: applyErr != nil} (always).
//  10. best-effort GateRevoke; return 0/1.
//
// With no server configured the gate check and finalize calls are no-ops, so a
// local run proceeds unchanged.
func runApply(args []string) int {
	fs := flag.NewFlagSet("run apply", flag.ContinueOnError)
	dir := fs.String("dir", "", "terramate project root (required)")
	changed := fs.Bool("changed", true, "only apply changed stacks")
	parallel := fs.Int("parallel", 0, "parallel apply jobs (0 = terramate default, serial); terramate still honors dependency order")
	base := fs.String("base", "", "git base ref for change detection")
	script := fs.String("script", "apply", "terramate script name to run")
	logFile := fs.String("log-file", "tfstackplan.log", "per-stack log filename the terramate script writes in each stack dir; streamed live to the server (empty disables)")
	stateLock := fs.Bool("state-lock", false, "acquire a pessimistic GCS state lock around cross-state moves (fail-fast; requires ADC)")
	impersonateRequester := fs.Bool("impersonate-requester", false, "run apply AS the leased PAM requester SA (mints GOOGLE_OAUTH_ACCESS_TOKEN for it)")
	cfgPath := fs.String("config", "", "HCL config for the classify pass (default: auto-discover .tfstackplan.hcl under --dir)")
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

	tm := newTerramate(*dir)
	execID := os.Getenv(runner.EnvExecution)
	if execID == "" {
		execID = fmt.Sprintf("apply-%d", time.Now().UnixNano())
	}

	// 1. Compute the changed stacks FIRST, before touching the gate. A merged
	//    bootstrap-only PR (zero changed stacks) must not 409 "not classified".
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

	// 2. NOTHING_TO_APPLY: no changed stacks — a no-op apply (e.g. a
	//    bootstrap-only / docs / cross-state-move-only merge). Finalize to a
	//    terminal success (driveApply resolves total==0 → success) and exit 0
	//    WITHOUT ever consulting the gate.
	if len(stacks) == 0 {
		_ = client.Finalize(ctx, events.Finalize{ID: execID})
		fmt.Fprintln(os.Stderr, "tfstackplan run apply: no changed stacks — nothing to apply")
		return 0
	}

	_ = client.Phase(ctx, events.PhaseEvent{ID: execID, Phase: events.PhaseApplying})

	// 4. Classify pass (self-sufficient): re-run the plan classification keyed to
	//    the same (pr, env) and submit it as a Finalize{Gates}. In reconciler-core
	//    mode the server re-marks gate_runs (classified) and issues RequestGrant;
	//    in legacy mode handleFinalize does requestGrants + MarkClassified. Either
	//    way this re-establishes the gate's classification + grant requests even if
	//    the serve DB was wiped since the PR's plan ran. Best-effort: a classify
	//    failure must not strand a recoverable apply — the fail-closed GateCheck
	//    below is the real guard.
	gates, categories, counts, moving, report, cerr := classifyForGateFn(ctx, *dir, stacks, *base, *changed, *cfgPath)
	if cerr != nil {
		// The classify pass is the precondition for trusting the gate state under
		// elevation. A *successful* classify with an empty requester legitimately
		// means "no gates — apply under the ambient SA"; but a *failed* classify
		// leaves that ambiguous. Proceeding under --impersonate-requester would
		// silently run every IAM-gated resource as the unelevated ambient build SA
		// and 403 despite an ACTIVE grant (PAM grants the role to the leased
		// requester, not the ambient SA) — the v0.12.0 fail-open this guards
		// against. Fail CLOSED: abort with a terminal cause and leave the grant
		// intact (no revoke) so a retry — classify flakes such as an unreachable
		// chart repo are transient — can reuse it, or break-glass per docs/ci-cd.md.
		if *impersonateRequester {
			fmt.Fprintln(os.Stderr, "tfstackplan run apply: classify pass failed under --impersonate-requester; refusing to apply unelevated:", cerr)
			_ = client.Finalize(ctx, events.Finalize{
				ID:             execID,
				Failed:         true,
				ReportMarkdown: "Classify pass failed before an elevated apply; refusing to proceed under the ambient identity (would 403 on gated resources despite an active grant). Retry this tier's apply — classify flakes are transient — or break-glass per docs/ci-cd.md. Cause: " + cerr.Error(),
			})
			return 1
		}
		fmt.Fprintln(os.Stderr, "tfstackplan run apply: classify pass failed (continuing to gate check):", cerr)
	} else {
		_ = client.Finalize(ctx, events.Finalize{
			ID: execID, ReportMarkdown: report, Gates: gates, Categories: categories, Counts: counts, Moving: moving,
		})
	}

	// 5. Fail-closed gate pre-check. GateCheck no-ops when no server is configured,
	//    and errors (fail closed) when a configured server is unreachable or the
	//    gate is unsatisfied.
	requester, gateErr := client.GateCheck(ctx, events.GateCheck{PR: pr, Environment: env})
	if gateErr != nil {
		printGateRejected(os.Stderr, gateErr, client, pr)
		return 1
	}

	// 6. Optionally run AS the leased requester SA.
	if *impersonateRequester && requester != "" {
		tok, err := mintAccessToken(ctx, requester)
		if err != nil {
			fmt.Fprintln(os.Stderr, "tfstackplan run apply: impersonate", requester, ":", err)
			return 1 // fail closed: asked to run elevated, could not
		}
		os.Setenv("GOOGLE_OAUTH_ACCESS_TOKEN", tok)
		fmt.Fprintln(os.Stderr, "tfstackplan run apply: applying AS", requester)
	}

	// 7. Fail-closed cross-state move pre-phase: execute any pending
	//    `_tfsp_xmove.*.hcl` manifests before the apply runs. No-op when none are
	//    present. A failure here aborts the apply (the moves must land cleanly,
	//    otherwise the apply would plan against a stale/half-moved state) and is
	//    surfaced to the check run as STATE_MOVE_FAILED.
	var stateLocker statemove.Locker
	if *stateLock {
		l, err := gcsLockerFromADC(ctx)
		if err != nil {
			fmt.Fprintln(os.Stderr, "tfstackplan run apply: --state-lock:", err)
			return 1
		}
		stateLocker = l
	}
	moveSink := func(stack, line string) {
		_ = client.LogChunk(ctx, events.LogChunk{ID: execID, Stack: stack, Data: line + "\n"})
	}
	if err := applyMovesFn(ctx, *dir, true, stateLocker, os.Stderr, moveSink); err != nil {
		fmt.Fprintln(os.Stderr, "tfstackplan run apply: cross-state move failed:", err)
		_ = client.Finalize(ctx, events.Finalize{
			ID:             execID,
			Failed:         true,
			ReportMarkdown: "cross-state move failed: " + err.Error(),
		})
		_ = client.GateRevoke(ctx, events.GateRevoke{PR: pr, Environment: env})
		return 1
	}

	// 8. Apply the changed stacks in dependency order via the terramate script.
	os.Setenv(runner.EnvExecution, execID)
	var stop func()
	if client.Enabled() && *logFile != "" {
		stop = runner.NewLogPump(client, *dir, *logFile, execID).Start(stacks)
	}
	// --parallel N runs the apply across stacks concurrently; terramate still
	// honors the dependency DAG, so a dependency applies before its dependents.
	// 0 (default) = terramate default (serial dependency order).
	applyErr := tm.ScriptRun(ctx, os.Stderr, runner.ScriptRunOptions{Script: *script, Changed: *changed, Parallel: *parallel, Base: *base})
	if stop != nil {
		stop()
	}

	// 9. Always emit a terminal Finalize. On failure the server marks any
	//    pending/running stacks failed and concludes the apply check run failure;
	//    on success it lets the check run conclude success terminally even if a
	//    per-stack `safe` tick was missed.
	_ = client.Finalize(ctx, events.Finalize{ID: execID, Failed: applyErr != nil})

	// 10. Best-effort post-apply cleanup: revoke the PR's grants.
	_ = client.GateRevoke(ctx, events.GateRevoke{PR: pr, Environment: env})

	if applyErr != nil {
		fmt.Fprintln(os.Stderr, "tfstackplan run apply: apply failed:", applyErr)
		return 1
	}
	return 0
}

// printGateRejected writes a classified, actionable next-steps message for a
// fail-closed gate rejection. It distinguishes a not-classified / awaiting-
// approval gate (→ approve at the live URL, then re-run the tier's apply) from a
// degraded/unreachable serve (→ break-glass local apply) from a generic
// gate-not-satisfied. The live URL + PR are included so the operator has a
// one-click path.
func printGateRejected(w io.Writer, gateErr error, client *runner.Client, pr int) {
	msg := gateErr.Error()
	server := os.Getenv(runner.EnvServer)
	fmt.Fprintln(w, "tfstackplan run apply: refusing to apply —", gateErr)
	switch {
	case strings.Contains(msg, "not classified") || strings.Contains(msg, "409") ||
		strings.Contains(msg, "AWAITING") || strings.Contains(msg, "awaiting"):
		fmt.Fprintf(w, "  state: AWAITING_APPROVAL — the PR's approval grant(s) are not yet active.\n")
		if server != "" {
			fmt.Fprintf(w, "  next: approve the grant(s) at %s (PR #%d), then re-run this tier's apply.\n", server, pr)
		} else {
			fmt.Fprintf(w, "  next: approve the grant(s) for PR #%d, then re-run this tier's apply.\n", pr)
		}
	case isUnreachable(msg):
		fmt.Fprintf(w, "  state: GATE_UNREACHABLE — the approval server is degraded/unreachable.\n")
		fmt.Fprintf(w, "  next: see the break-glass local-apply runbook in docs/ci-cd.md.\n")
	default:
		fmt.Fprintf(w, "  state: gate not satisfied.\n")
		if server != "" {
			fmt.Fprintf(w, "  next: review the gate at %s (PR #%d), then re-run this tier's apply.\n", server, pr)
		}
	}
}

// isUnreachable heuristically classifies a gate error as a transport/5xx failure
// (serve down) rather than a 409 not-classified / unapproved gate.
func isUnreachable(msg string) bool {
	for _, m := range []string{"connection refused", "no such host", "timeout", "Timeout", "EOF",
		": 500:", ": 502:", ": 503:", ": 504:", "context deadline exceeded"} {
		if strings.Contains(msg, m) {
			return true
		}
	}
	return false
}
