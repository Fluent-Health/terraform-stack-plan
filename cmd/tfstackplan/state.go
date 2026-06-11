package main

import (
	"cmp"
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
	stack := fs.String("stack", "", "default stack for unqualified addresses (same-stack moves)")
	pr := fs.String("pr", "", "PR number for the shim key (default: $TFSTACKPLAN_PR or git branch)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "state move: --dir is required")
		return 2
	}
	rest := fs.Args()
	if len(rest) == 0 || len(rest)%2 != 0 {
		fmt.Fprintln(os.Stderr, "state move: expected <from> <to> pairs")
		return 2
	}

	plans := map[string]*tfjson.Plan{}
	loadFor := func(s string) (*tfjson.Plan, error) {
		if p, ok := plans[s]; ok {
			return p, nil
		}
		p, err := loadStatePlan(filepath.Join(*dir, filepath.FromSlash(s)))
		if err != nil {
			return nil, fmt.Errorf("stack %q: %w", s, err)
		}
		plans[s] = p
		return p, nil
	}
	stackOf := func(qualified string) (string, string, error) {
		s, addr := stripStack(qualified)
		if s == "" {
			s = *stack
		}
		if s == "" {
			return "", "", fmt.Errorf("address %q has no stack (use stack:addr or --stack)", qualified)
		}
		return s, addr, nil
	}

	opsByStack := map[string][]statemove.Op{}
	for i := 0; i < len(rest); i += 2 {
		fromStack, fromAddr, e1 := stackOf(rest[i])
		toStack, toAddr, e2 := stackOf(rest[i+1])
		if e1 != nil || e2 != nil {
			fmt.Fprintln(os.Stderr, "state move:", cmp.Or(e1, e2))
			return 2
		}
		if fromStack == toStack {
			plan, err := loadFor(fromStack)
			if err != nil {
				fmt.Fprintln(os.Stderr, "state move:", err)
				return 1
			}
			if err := statemove.ValidateMove(plan, fromAddr, toAddr); err != nil {
				fmt.Fprintln(os.Stderr, "state move:", err)
				return 1
			}
			opsByStack[fromStack] = append(opsByStack[fromStack], statemove.Op{Kind: "moved", From: fromAddr, To: toAddr})
			continue
		}
		srcPlan, e3 := loadFor(fromStack)
		dstPlan, e4 := loadFor(toStack)
		if e3 != nil || e4 != nil {
			fmt.Fprintln(os.Stderr, "state move:", cmp.Or(e3, e4))
			return 1
		}
		srcOps, dstOps, err := statemove.ClassifyCrossStack(srcPlan, dstPlan, fromAddr, toAddr)
		if err != nil {
			fmt.Fprintln(os.Stderr, "state move:", err)
			return 1
		}
		opsByStack[fromStack] = append(opsByStack[fromStack], srcOps...)
		opsByStack[toStack] = append(opsByStack[toStack], dstOps...)
	}

	key := moveKey(*pr, *dir)
	for s, ops := range opsByStack {
		shimPath := filepath.Join(*dir, filepath.FromSlash(s), statemove.ShimFileName(key))
		var existing []statemove.Op
		if data, rerr := os.ReadFile(shimPath); rerr == nil {
			if _, ex, perr := statemove.ParseShim(string(data)); perr == nil {
				existing = ex
			}
		}
		merged := statemove.MergeOps(existing, ops)
		if werr := os.WriteFile(shimPath, []byte(statemove.RenderShim(key, merged)), 0o644); werr != nil {
			fmt.Fprintln(os.Stderr, "state move: write shim:", werr)
			return 1
		}
		fmt.Printf("wrote %s (%d op(s))\n", shimPath, len(merged))
	}
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
			switch o.Kind {
			case "moved":
				fmt.Printf("%s\t%s\tmoved %s → %s\n", s.Key, s.Stack, o.From, o.To)
			case "import":
				fmt.Printf("%s\t%s\timport %s (id=%s)\n", s.Key, s.Stack, o.To, o.ID)
			case "removed":
				fmt.Printf("%s\t%s\tremoved %s\n", s.Key, s.Stack, o.From)
			}
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
