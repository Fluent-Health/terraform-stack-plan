package main

import (
	"cmp"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
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
		fmt.Fprintln(os.Stderr, "tfstackplan state: expected a subcommand (move|list|moves-manifest|cleanup|apply)")
		return 2
	}
	switch args[0] {
	case "move":
		return runStateMove(args[1:])
	case "list":
		return runStateList(args[1:])
	case "moves-manifest":
		return runStateMovesManifest(args[1:])
	case "cleanup":
		return runStateCleanup(args[1:])
	case "apply":
		return runStateApply(args[1:])
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
	via := fs.String("via", "", "cross-stack mechanism: \"\" (native import/removed) or \"mv\" (faithful terraform state mv)")
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
	xmoveByDest := map[string]statemove.XMove{}
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
		// Cross-stack path: load source plan first.
		srcPlan, e3 := loadFor(fromStack)
		if e3 != nil {
			fmt.Fprintln(os.Stderr, "state move:", e3)
			return 1
		}

		if *via == "mv" {
			if err := statemove.CheckXMoveSource(srcPlan, fromAddr); err != nil {
				fmt.Fprintln(os.Stderr, "state move:", err)
				return 1
			}
			existing, ok := xmoveByDest[toStack]
			if ok && existing.SourceStack != fromStack {
				fmt.Fprintf(os.Stderr, "state move: dest stack %q already targets source %q; cannot also pull from %q (one manifest per dest+source)\n", toStack, existing.SourceStack, fromStack)
				return 1
			}
			existing.SourceStack = fromStack
			existing.Pairs = append(existing.Pairs, statemove.Move{From: fromAddr, To: toAddr})
			xmoveByDest[toStack] = existing
			continue
		}

		// Non-mv cross-stack: also load dest plan for ClassifyCrossStack.
		dstPlan, e4 := loadFor(toStack)
		if e4 != nil {
			fmt.Fprintln(os.Stderr, "state move:", e4)
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
	for destStack, xm := range xmoveByDest {
		manifestPath := filepath.Join(*dir, filepath.FromSlash(destStack), statemove.XMoveFileName(key))
		merged := xm
		if data, rerr := os.ReadFile(manifestPath); rerr == nil {
			_, prev, perr := statemove.ParseXMove(string(data))
			if perr != nil {
				fmt.Fprintf(os.Stderr, "state move: parse existing %s: %v\n", manifestPath, perr)
				return 1
			}
			if prev.SourceStack != xm.SourceStack {
				fmt.Fprintf(os.Stderr, "state move: existing manifest %s targets source %q, not %q\n", manifestPath, prev.SourceStack, xm.SourceStack)
				return 1
			}
			merged.Pairs = mergeMoves(prev.Pairs, xm.Pairs)
		}
		if werr := os.WriteFile(manifestPath, []byte(statemove.RenderXMove(key, merged)), 0o644); werr != nil {
			fmt.Fprintln(os.Stderr, "state move: write xmove:", werr)
			return 1
		}
		fmt.Printf("wrote %s (%d move(s))\n", manifestPath, len(merged.Pairs))
	}
	return 0
}

// mergeMoves appends add to existing, de-duplicating whole pairs by From+To,
// preserving order.
func mergeMoves(existing, add []statemove.Move) []statemove.Move {
	seen := map[statemove.Move]bool{}
	out := make([]statemove.Move, 0, len(existing)+len(add))
	for _, m := range append(append([]statemove.Move{}, existing...), add...) {
		if seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	return out
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

func runStateApply(args []string) int {
	fs := flag.NewFlagSet("state apply", flag.ContinueOnError)
	dir := fs.String("dir", "", "terramate project root (required)")
	execute := fs.Bool("execute", false, "perform the moves (default: dry-run, print only)")
	lock := fs.Bool("lock", false, "acquire a pessimistic GCS state lock around each move (fail-fast if already locked; requires ADC)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "state apply: --dir is required")
		return 2
	}
	var locker statemove.Locker
	if *lock && *execute {
		l, err := gcsLockerFromADC(context.Background())
		if err != nil {
			fmt.Fprintln(os.Stderr, "state apply: --lock:", err)
			return 1
		}
		locker = l
	}
	if err := applyPendingMoves(context.Background(), *dir, *execute, locker, os.Stdout, nil); err != nil {
		fmt.Fprintln(os.Stderr, "state apply:", err)
		return 1
	}
	return 0
}

// gcsLockerFromADC builds a GCS state Locker from Application Default
// Credentials (the same token source the planner/applier uses).
func gcsLockerFromADC(ctx context.Context) (statemove.Locker, error) {
	token, _, err := gcpCreds(ctx)
	if err != nil {
		return nil, err
	}
	return newGCSLocker(token, ""), nil
}

// buildExecDeps resolves the terraform binary and builds the ExecDeps for
// applyPendingMoves. A package var so tests can inject a fake runner without a
// real terraform binary or GCS backend (override returns a non-nil ExecDeps and
// a nil error to bypass the real LookPath + NewTerraform).
var buildExecDeps = func(dir string, locker statemove.Locker) (statemove.ExecDeps, error) {
	tfPath, err := exec.LookPath("terraform")
	if err != nil {
		return statemove.ExecDeps{}, fmt.Errorf("terraform not found on PATH: %w", err)
	}
	return statemove.ExecDeps{
		NewTF:     func(wd string) (statemove.Runner, error) { return statemove.NewTerraform(tfPath, wd) },
		BackupDir: filepath.Join(dir, ".tfsp-state-backups"),
		Locker:    locker,
	}, nil
}

// applyPendingMoves discovers and runs every pending `_tfsp_xmove.*.hcl`
// cross-state move manifest under dir. It is shared by `state apply` and the
// `run apply` pre-phase. With execute=false it dry-runs (prints what it would
// do). It is fail-closed: a terraform binary is required only when moves are
// pending, and the first Execute error aborts the whole run. A nil locker
// means no pessimistic lock. sink (may be nil) receives each formatted move
// line keyed to the destination stack for per-stack log streaming.
func applyPendingMoves(ctx context.Context, dir string, execute bool, locker statemove.Locker, w io.Writer, sink func(stack, line string)) error {
	found, err := statemove.DiscoverXMoves(dir)
	if err != nil {
		return err
	}
	if len(found) == 0 {
		return nil
	}
	deps, err := buildExecDeps(dir, locker)
	if err != nil {
		return fmt.Errorf("%d pending cross-state move(s): %w", len(found), err)
	}
	for _, fx := range found {
		actions, err := statemove.Execute(ctx, deps, dir, fx.DestStack, fx.XMove, !execute)
		if err != nil {
			return fmt.Errorf("%s (%s → %s): %w", fx.Key, fx.XMove.SourceStack, fx.DestStack, err)
		}
		for _, a := range actions {
			verb := "would move"
			if a.Decision == statemove.DecisionSkip {
				verb = "skip (already moved)"
			} else if execute {
				verb = "moved"
			}
			line := fmt.Sprintf("%s\t%s → %s\t%s %s → %s", fx.Key, fx.XMove.SourceStack, fx.DestStack, verb, a.From, a.To)
			fmt.Fprintln(w, line)
			if sink != nil {
				sink(fx.DestStack, line)
			}
		}
	}
	return nil
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
	nx, err := statemove.CleanupXMoves(*dir, key)
	if err != nil {
		fmt.Fprintln(os.Stderr, "state cleanup xmoves:", err)
		return 1
	}
	fmt.Printf("removed %d shim file(s), %d xmove manifest(s)\n", n, nx)
	return 0
}
