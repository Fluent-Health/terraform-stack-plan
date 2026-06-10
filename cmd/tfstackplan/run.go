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
		fmt.Fprintln(os.Stderr, "tfstackplan run: expected a subcommand (tick|plan)")
		return 2
	}
	switch args[0] {
	case "tick":
		return runTick(args[1:])
	case "plan":
		return runPlan(args[1:])
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
