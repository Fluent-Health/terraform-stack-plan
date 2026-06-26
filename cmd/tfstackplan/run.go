package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/runner"
)

// runRun dispatches the `run` subcommand group (tick now; plan/apply later).
func runRun(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "tfstackplan run: expected a subcommand (tick|phase|step|register|exec|lint|plan|apply|verify|claims)")
		return 2
	}
	switch args[0] {
	case "tick":
		return runTick(args[1:])
	case "phase":
		return runPhase(args[1:])
	case "step":
		return runStep(args[1:])
	case "register":
		return runRegister(args[1:])
	case "exec":
		return runExec(args[1:])
	case "plan":
		return runPlan(args[1:])
	case "lint":
		return runLint(args[1:])
	case "apply":
		return runApply(args[1:])
	case "verify":
		return runVerify(args[1:])
	case "claims":
		return runClaims(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "tfstackplan run: unknown subcommand %q\n", args[0])
		return 2
	}
}

// runTick reports one stack's status to the server (best-effort). It reads the
// execution context from the environment (set by the orchestrator) and is a
// no-op when no server is configured, so a human's `terramate script run` works
// offline. It always exits 0 except on a flag-parse error — a progress tick must
// never fail the build.
func runTick(args []string) int {
	fs := flag.NewFlagSet("run tick", flag.ContinueOnError)
	stack := fs.String("stack", "", "stack path (defaults to $"+runner.EnvStack+")")
	status := fs.String("status", "", "stack status (e.g. running, planned, failed)")
	detail := fs.String("detail", "", "optional failure detail")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	s := *stack
	if s == "" {
		s = os.Getenv(runner.EnvStack)
	}
	execID := os.Getenv(runner.EnvExecution)
	if *status == "" || s == "" || execID == "" {
		return 0
	}
	c := runner.ClientFromEnv()
	_ = c.Update(context.Background(), events.Update{
		ID:     execID,
		Stack:  s,
		Status: events.Status(*status),
		Detail: *detail,
	})
	return 0
}

// runPhase narrates a lifecycle phase to the server (best-effort). It reads the
// execution context from the environment (set by the CI step) and is a no-op when
// no execution id or server is configured, so a human's local run is unaffected.
// It always exits 0 except on a flag-parse error — a progress tick must never fail
// the build. Emitted before `run plan` (warming/initializing), it lets the server
// create the per-environment check run during cache warm-up rather than only once
// planning starts.
func runPhase(args []string) int {
	fs := flag.NewFlagSet("run phase", flag.ContinueOnError)
	phase := fs.String("phase", "", "lifecycle phase (warming|linting|initializing|planning|applying|testing|verifying)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	execID := os.Getenv(runner.EnvExecution)
	if *phase == "" || execID == "" {
		return 0
	}
	if !events.Phase(*phase).Valid() {
		fmt.Fprintf(os.Stderr, "tfstackplan run phase: unknown phase %q\n", *phase)
		return 2
	}
	c := runner.ClientFromEnv()
	_ = c.Phase(context.Background(), events.PhaseEvent{
		ID:          execID,
		Repo:        os.Getenv("TFSTACKPLAN_REPO"),
		SHA:         os.Getenv("TFSTACKPLAN_SHA"),
		PR:          atoiOr0(os.Getenv("TFSTACKPLAN_PR")),
		Environment: os.Getenv(runner.EnvEnvironment),
		Phase:       events.Phase(*phase),
	})
	return 0
}
