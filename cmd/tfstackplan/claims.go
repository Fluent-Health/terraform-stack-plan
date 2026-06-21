package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/Fluent-Health/terraform-stack-plan/internal/runner"
)

// runClaims dispatches the `claims` subcommand group (list|release).
func runClaims(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "tfstackplan claims: expected a subcommand (list|release)")
		return 2
	}
	switch args[0] {
	case "list":
		return runClaimsList(args[1:])
	case "release":
		return runClaimsRelease(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "tfstackplan claims: unknown subcommand %q\n", args[0])
		return 2
	}
}

// runClaimsList prints the current apply-lock claims for an environment.
func runClaimsList(args []string) int {
	fs := flag.NewFlagSet("claims list", flag.ContinueOnError)
	env := fs.String("env", "", "environment name (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *env == "" {
		fmt.Fprintln(os.Stderr, "tfstackplan claims list: --env is required")
		return 2
	}
	client := runner.ClientFromEnv()
	if !client.Enabled() {
		fmt.Fprintln(os.Stderr, "tfstackplan claims list: no server configured (TFSTACKPLAN_SERVER unset)")
		return 0
	}
	claims, err := client.ClaimsList(context.Background(), *env)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tfstackplan claims list:", err)
		return 1
	}
	if len(claims) == 0 {
		fmt.Printf("No apply-lock claims for environment %q\n", *env)
		return 0
	}
	fmt.Printf("Apply-lock claims for environment %q:\n", *env)
	for _, c := range claims {
		fmt.Printf("  stack=%-40s  pr=#%d  expires=%s\n", c.StackPath, c.OwnerPR, c.ExpiresAt.Format("2006-01-02T15:04:05Z"))
	}
	return 0
}

// runClaimsRelease releases one stack's claim or all claims for a PR in an env.
func runClaimsRelease(args []string) int {
	fs := flag.NewFlagSet("claims release", flag.ContinueOnError)
	env := fs.String("env", "", "environment name (required)")
	pr := fs.Int("pr", 0, "PR number whose claim(s) to release (required)")
	stack := fs.String("stack", "", "stack path to release (omit to release all stacks for the PR)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *env == "" || *pr == 0 {
		fmt.Fprintln(os.Stderr, "tfstackplan claims release: --env and --pr are required")
		return 2
	}
	client := runner.ClientFromEnv()
	if !client.Enabled() {
		fmt.Fprintln(os.Stderr, "tfstackplan claims release: no server configured (TFSTACKPLAN_SERVER unset)")
		return 0
	}
	if err := client.ClaimsRelease(context.Background(), *env, *pr, *stack); err != nil {
		fmt.Fprintln(os.Stderr, "tfstackplan claims release:", err)
		return 1
	}
	if *stack != "" {
		fmt.Printf("Released apply-lock claim: env=%s pr=#%d stack=%s\n", *env, *pr, *stack)
	} else {
		fmt.Printf("Released all apply-lock claims: env=%s pr=#%d\n", *env, *pr)
	}
	return 0
}
