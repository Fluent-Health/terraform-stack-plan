package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/Fluent-Health/terraform-stack-plan/internal/runner"
)

func runAdmin(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "tfstackplan admin: expected a subcommand (grants|executions|gates|checks)")
		return 2
	}
	switch args[0] {
	case "grants":
		return runAdminGrants(args[1:])
	case "executions":
		return runAdminExecutions(args[1:])
	case "gates":
		return runAdminGates(args[1:])
	case "checks":
		return runAdminChecks(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "tfstackplan admin: unknown subcommand %q\n", args[0])
		return 2
	}
}

func runAdminGrants(args []string) int {
	if len(args) == 0 || args[0] != "release" {
		fmt.Fprintln(os.Stderr, "tfstackplan admin grants: expected 'release'")
		return 2
	}
	fs := flag.NewFlagSet("admin grants release", flag.ContinueOnError)
	pr := fs.Int("pr", 0, "PR number (required)")
	env := fs.String("env", "", "environment name (required)")
	class := fs.String("class", "", "grant class (required)")
	target := fs.String("target", "", "grant target (required)")
	reason := fs.String("reason", "admin intervention", "reason for release")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if *pr == 0 || *env == "" || *class == "" || *target == "" {
		fmt.Fprintln(os.Stderr, "tfstackplan admin grants release: --pr, --env, --class, --target are required")
		return 2
	}
	client := runner.ClientFromEnv()
	if !client.Enabled() {
		fmt.Fprintln(os.Stderr, "tfstackplan admin: server unset")
		return 1
	}
	if err := client.AdminGrantsRelease(context.Background(), *pr, *env, *class, *target, *reason); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	fmt.Println("Success")
	return 0
}

func runAdminExecutions(args []string) int {
	if len(args) == 0 || args[0] != "cancel" {
		fmt.Fprintln(os.Stderr, "tfstackplan admin executions: expected 'cancel'")
		return 2
	}
	fs := flag.NewFlagSet("admin executions cancel", flag.ContinueOnError)
	id := fs.String("id", "", "execution ID (required)")
	reason := fs.String("reason", "admin intervention", "reason for cancellation")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if *id == "" {
		fmt.Fprintln(os.Stderr, "tfstackplan admin executions cancel: --id is required")
		return 2
	}
	client := runner.ClientFromEnv()
	if !client.Enabled() {
		fmt.Fprintln(os.Stderr, "tfstackplan admin: server unset")
		return 1
	}
	if err := client.AdminExecutionsCancel(context.Background(), *id, *reason); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	fmt.Println("Success")
	return 0
}

func runAdminGates(args []string) int {
	if len(args) == 0 || args[0] != "satisfy" {
		fmt.Fprintln(os.Stderr, "tfstackplan admin gates: expected 'satisfy'")
		return 2
	}
	fs := flag.NewFlagSet("admin gates satisfy", flag.ContinueOnError)
	pr := fs.Int("pr", 0, "PR number (required)")
	env := fs.String("env", "", "environment name (required)")
	class := fs.String("class", "", "grant class (required)")
	target := fs.String("target", "", "grant target (required)")
	reason := fs.String("reason", "admin intervention", "reason for satisfy")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if *pr == 0 || *env == "" || *class == "" || *target == "" {
		fmt.Fprintln(os.Stderr, "tfstackplan admin gates satisfy: --pr, --env, --class, --target are required")
		return 2
	}
	client := runner.ClientFromEnv()
	if !client.Enabled() {
		fmt.Fprintln(os.Stderr, "tfstackplan admin: server unset")
		return 1
	}
	if err := client.AdminGatesSatisfy(context.Background(), *pr, *env, *class, *target, *reason); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	fmt.Println("Success")
	return 0
}

func runAdminChecks(args []string) int {
	if len(args) == 0 || args[0] != "override" {
		fmt.Fprintln(os.Stderr, "tfstackplan admin checks: expected 'override'")
		return 2
	}
	fs := flag.NewFlagSet("admin checks override", flag.ContinueOnError)
	pr := fs.Int("pr", 0, "PR number (required)")
	env := fs.String("env", "", "environment name (required)")
	check := fs.String("check", "", "check name (required)")
	conclusion := fs.String("conclusion", "", "override conclusion (required)")
	reason := fs.String("reason", "admin intervention", "reason for override")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if *pr == 0 || *env == "" || *check == "" || *conclusion == "" {
		fmt.Fprintln(os.Stderr, "tfstackplan admin checks override: --pr, --env, --check, --conclusion are required")
		return 2
	}
	client := runner.ClientFromEnv()
	if !client.Enabled() {
		fmt.Fprintln(os.Stderr, "tfstackplan admin: server unset")
		return 1
	}
	if err := client.AdminChecksOverride(context.Background(), *pr, *env, *check, *conclusion, *reason); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	fmt.Println("Success")
	return 0
}
