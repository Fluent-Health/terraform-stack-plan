package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Fluent-Health/terraform-stack-plan/internal/config"
	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
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

// runClaimsList prints the current apply-lock claims for one or all environments.
func runClaimsList(args []string) int {
	fs := flag.NewFlagSet("claims list", flag.ContinueOnError)
	env := fs.String("env", "", "environment name (optional; defaults to discovering from repository)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	var envs []string
	if *env != "" {
		envs = []string{*env}
	} else {
		envs = discoverEnvironments(".")
		if len(envs) == 0 {
			fmt.Fprintln(os.Stderr, "tfstackplan claims list: --env is required or environments must be discoverable from the repository layout")
			return 2
		}
	}

	hasEnabledServer := false
	var fetchErrors []string
	type envClaims struct {
		env    string
		claims []events.Claim
	}
	var results []envClaims

	for _, e := range envs {
		client := runner.ClientForEnvironment(e)
		if !client.Enabled() {
			continue
		}
		hasEnabledServer = true
		claims, err := client.ClaimsList(context.Background(), e)
		if err != nil {
			fetchErrors = append(fetchErrors, fmt.Sprintf("%s: %v", e, err))
			continue
		}
		results = append(results, envClaims{env: e, claims: claims})
	}

	if !hasEnabledServer {
		fmt.Fprintln(os.Stderr, "tfstackplan claims list: no server configured (TFSTACKPLAN_SERVER unset)")
		return 1
	}

	if len(fetchErrors) > 0 {
		for _, fe := range fetchErrors {
			fmt.Fprintln(os.Stderr, "tfstackplan claims list error:", fe)
		}
		return 1
	}

	for _, r := range results {
		if len(r.claims) == 0 {
			fmt.Printf("No apply-lock claims for environment %q\n", r.env)
		} else {
			fmt.Printf("Apply-lock claims for environment %q:\n", r.env)
			for _, c := range r.claims {
				fmt.Printf("  stack=%-40s  pr=#%d  expires=%s\n", c.StackPath, c.OwnerPR, c.ExpiresAt.Format("2006-01-02T15:04:05Z"))
			}
		}
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
	client := runner.ClientForEnvironment(*env)
	if !client.Enabled() {
		fmt.Fprintln(os.Stderr, "tfstackplan claims release: no server configured (TFSTACKPLAN_SERVER unset)")
		return 1
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

func discoverEnvironments(dir string) []string {
	// 1. Try to read subdirectories under `stacks/`
	stacksPath := filepath.Join(dir, "stacks")
	entries, err := os.ReadDir(stacksPath)
	if err == nil {
		var envs []string
		for _, e := range entries {
			if e.IsDir() {
				envs = append(envs, e.Name())
			}
		}
		if len(envs) > 0 {
			return envs
		}
	}

	// 2. Fallback to parsing stacks from terramate list
	tm := &runner.Terramate{Dir: dir}
	stacks, err := tm.List(context.Background())
	if err == nil {
		envMap := map[string]bool{}
		for _, s := range stacks {
			parts := strings.Split(s, "/")
			if len(parts) > 1 && parts[0] == "stacks" {
				envMap[parts[1]] = true
			}
		}
		if len(envMap) > 0 {
			var envs []string
			for env := range envMap {
				envs = append(envs, env)
			}
			return envs
		}
	}

	// 3. Fallback to any environment defined in the .tfstackplan.hcl servers config!
	if p, ok := config.Discover(dir); ok {
		if cfg, err := config.Load(p); err == nil {
			envMap := map[string]bool{}
			for _, s := range cfg.Servers {
				if s.Environment != "" {
					envMap[s.Environment] = true
				}
				if s.Name != "" {
					envMap[s.Name] = true
				}
			}
			if len(envMap) > 0 {
				var envs []string
				for env := range envMap {
					envs = append(envs, env)
				}
				return envs
			}
		}
	}

	return nil
}
