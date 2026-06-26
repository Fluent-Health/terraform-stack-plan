package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/runner"
)

// runExec runs a single command with optional lifecycle phase narration and
// fail-closed check-run finalization. It is a transparent passthrough when no
// server is configured (TFSTACKPLAN_SERVER unset), so local runs are unaffected.
// Phase tick and Finalize are best-effort — server errors never fail the command.
func runExec(args []string) int {
	fs := flag.NewFlagSet("run exec", flag.ContinueOnError)
	phase := fs.String("phase", "", "lifecycle phase to tick before running (warming|linting|initializing|planning|applying|testing|verifying)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cmdArgs := fs.Args()
	if len(cmdArgs) == 0 {
		fmt.Fprintln(os.Stderr, "tfstackplan run exec: command is required (use -- <cmd> [args...])")
		return 2
	}
	if *phase != "" && !events.Phase(*phase).Valid() {
		fmt.Fprintf(os.Stderr, "tfstackplan run exec: unknown phase %q\n", *phase)
		return 2
	}

	execID := os.Getenv(runner.EnvExecution)
	client := runner.ClientFromEnv()
	ctx := context.Background()

	if *phase != "" && execID != "" {
		_ = client.Phase(ctx, events.PhaseEvent{
			ID:          execID,
			Repo:        os.Getenv("TFSTACKPLAN_REPO"),
			SHA:         os.Getenv("TFSTACKPLAN_SHA"),
			PR:          atoiOr0(os.Getenv("TFSTACKPLAN_PR")),
			Environment: os.Getenv(runner.EnvEnvironment),
			Phase:       events.Phase(*phase),
		})
	}

	cmd := exec.CommandContext(ctx, cmdArgs[0], cmdArgs[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if execID != "" {
			_ = client.Finalize(ctx, events.Finalize{ID: execID, Failed: true})
		}
		return exitCodeOf(err)
	}
	return 0
}
