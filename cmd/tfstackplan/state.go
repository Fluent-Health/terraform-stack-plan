package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	tfjson "github.com/hashicorp/terraform-json"

	"github.com/Fluent-Health/terraform-stack-plan/internal/statemove"
)

// runState dispatches the `state` subcommand group: declarative cross-stack move
// machinery. SP1 implements same-stack moves (native `moved {}` shims).
func runState(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "tfstackplan state: expected a subcommand (move|list|cleanup)")
		return 2
	}
	switch args[0] {
	case "move":
		return runStateMove(args[1:])
	case "list":
		return runStateList(args[1:])
	case "cleanup":
		return runStateCleanup(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "tfstackplan state: unknown subcommand %q\n", args[0])
		return 2
	}
}

// moveKey resolves the PR/commit key: --pr, else $TFSTACKPLAN_PR, else the git
// branch name (sanitized). Returns "PR-<n>" or "branch-<name>" (or "local").
func moveKey(prFlag, dir string) string {
	pr := prFlag
	if pr == "" {
		pr = os.Getenv("TFSTACKPLAN_PR")
	}
	if pr != "" {
		return "PR-" + pr
	}
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = dir
	if out, err := cmd.Output(); err == nil {
		b := strings.NewReplacer("/", "-", " ", "-").Replace(strings.TrimSpace(string(out)))
		if b != "" && b != "HEAD" {
			return "branch-" + b
		}
	}
	return "local"
}

// stripStack splits an optional "stack:addr" prefix. Terraform addresses contain
// no ":", so the first ":" is the stack boundary.
func stripStack(s string) (stack, addr string) {
	if i := strings.IndexByte(s, ':'); i >= 0 {
		return s[:i], s[i+1:]
	}
	return "", s
}

func loadStatePlan(stackDir string) (*tfjson.Plan, error) {
	data, err := os.ReadFile(filepath.Join(stackDir, "tfplan.json"))
	if err != nil {
		return nil, fmt.Errorf("read plan: %w (run `tfstackplan run plan` first)", err)
	}
	var p tfjson.Plan
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("parse plan: %w", err)
	}
	return &p, nil
}

func runStateMove(args []string) int {
	fs := flag.NewFlagSet("state move", flag.ContinueOnError)
	dir := fs.String("dir", "", "terramate project root (required)")
	stack := fs.String("stack", "", "stack path the moves are within (required for SP1)")
	pr := fs.String("pr", "", "PR number for the shim key (default: $TFSTACKPLAN_PR or git branch)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dir == "" || *stack == "" {
		fmt.Fprintln(os.Stderr, "state move: --dir and --stack are required")
		return 2
	}
	rest := fs.Args()
	if len(rest) == 0 || len(rest)%2 != 0 {
		fmt.Fprintln(os.Stderr, "state move: expected <from> <to> pairs")
		return 2
	}

	var newOps []statemove.Op
	for i := 0; i < len(rest); i += 2 {
		fromStack, from := stripStack(rest[i])
		toStack, to := stripStack(rest[i+1])
		for _, s := range []string{fromStack, toStack} {
			if s != "" && s != *stack {
				fmt.Fprintf(os.Stderr, "state move: cross-stack move %s → %s is not supported in SP1 (only --stack %q)\n", rest[i], rest[i+1], *stack)
				return 1
			}
		}
		newOps = append(newOps, statemove.Op{Kind: "moved", From: from, To: to})
	}

	stackDir := filepath.Join(*dir, filepath.FromSlash(*stack))
	plan, err := loadStatePlan(stackDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "state move:", err)
		return 1
	}
	for _, o := range newOps {
		if err := statemove.ValidateMove(plan, o.From, o.To); err != nil {
			fmt.Fprintln(os.Stderr, "state move:", err)
			return 1
		}
	}

	key := moveKey(*pr, *dir)
	shimPath := filepath.Join(stackDir, statemove.ShimFileName(key))
	var existing []statemove.Op
	if data, rerr := os.ReadFile(shimPath); rerr == nil {
		if _, ops, perr := statemove.ParseShim(string(data)); perr == nil {
			existing = ops
		}
	}
	merged := statemove.MergeOps(existing, newOps)
	if werr := os.WriteFile(shimPath, []byte(statemove.RenderShim(key, merged)), 0o644); werr != nil {
		fmt.Fprintln(os.Stderr, "state move: write shim:", werr)
		return 1
	}
	fmt.Printf("wrote %s (%d move(s))\n", shimPath, len(merged))
	return 0
}

func runStateList(args []string) int {
	fs := flag.NewFlagSet("state list", flag.ContinueOnError)
	dir := fs.String("dir", "", "terramate project root (required)")
	pr := fs.String("pr", "", "only this PR's moves")
	_ = fs.Bool("all", false, "list all moves (default)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "state list: --dir is required")
		return 2
	}
	shims, err := statemove.Discover(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "state list:", err)
		return 1
	}
	want := ""
	if *pr != "" {
		want = "PR-" + *pr
	}
	for _, s := range shims {
		if want != "" && s.Key != want {
			continue
		}
		for _, o := range s.Ops {
			fmt.Printf("%s\t%s\t%s %s → %s\n", s.Key, s.Stack, o.Kind, o.From, o.To)
		}
	}
	return 0
}

func runStateCleanup(args []string) int {
	fs := flag.NewFlagSet("state cleanup", flag.ContinueOnError)
	dir := fs.String("dir", "", "terramate project root (required)")
	pr := fs.String("pr", "", "remove only this PR's shims")
	all := fs.Bool("all", false, "remove ALL tfstackplan move shims in the tree")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "state cleanup: --dir is required")
		return 2
	}
	if (*pr == "") == (!*all) {
		fmt.Fprintln(os.Stderr, "state cleanup: pass exactly one of --pr <n> or --all")
		return 2
	}
	key := ""
	if *pr != "" {
		key = "PR-" + *pr
	}
	n, err := statemove.Cleanup(*dir, key)
	if err != nil {
		fmt.Fprintln(os.Stderr, "state cleanup:", err)
		return 1
	}
	fmt.Printf("removed %d shim file(s)\n", n)
	return 0
}
