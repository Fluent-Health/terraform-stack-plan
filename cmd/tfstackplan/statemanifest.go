package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Fluent-Health/terraform-stack-plan/internal/statemove"
)

// runStateMovesManifest discovers every pending move shim + xmove manifest under
// --dir and emits a two-sided --state-moves JSON ({"<stack>":["<addr>",…]}): the
// destination move-ins (which plan as creates) AND the source move-outs (which
// plan as destroys). Feeding this to `render/classify --state-moves` makes both
// sides classify as 🚚 moves, so a cross-state move of an IAM resource no longer
// trips the IAM gate on the *source* stack for an apply that is 0-diff.
func runStateMovesManifest(args []string) int {
	fs := flag.NewFlagSet("state moves-manifest", flag.ContinueOnError)
	dir := fs.String("dir", "", "terramate project root (required)")
	pr := fs.String("pr", "", "only this PR's moves (default: all)")
	out := fs.String("o", "", "write JSON to this file (default: stdout)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "state moves-manifest: --dir is required")
		return 2
	}
	want := ""
	if *pr != "" {
		want = "PR-" + *pr
	}

	manifest, err := collectStateMoves(*dir, want)
	if err != nil {
		fmt.Fprintln(os.Stderr, "state moves-manifest:", err)
		return 1
	}

	b, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "state moves-manifest:", err)
		return 1
	}
	b = append(b, '\n')
	if *out == "" {
		os.Stdout.Write(b)
		return 0
	}
	if err := os.WriteFile(*out, b, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "state moves-manifest:", err)
		return 1
	}
	return 0
}

// resolveSourceStack resolves an xmove source_stack path to a canonical slash
// path relative to root. When the xmove was generated with a --dir sub-tree
// (e.g. "stacks/nonprod"), source_stack is relative to that sub-tree rather than
// root. Walk destStack's ancestor prefixes from root downward until joining that
// prefix with source gives an existing directory; return that full path. If no
// ancestor resolves to a real directory, return source as-is (handles absolute
// paths or non-standard layouts).
func resolveSourceStack(root, destStack, source string) string {
	parts := strings.Split(destStack, "/")
	for i := 0; i < len(parts); i++ {
		var candidate string
		if i == 0 {
			candidate = source
		} else {
			candidate = strings.Join(parts[:i], "/") + "/" + source
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(candidate))); err == nil {
			return candidate
		}
	}
	return source
}

// collectStateMoves discovers every pending move shim + xmove manifest under dir
// and returns the two-sided state-moves map ({"<stack>":["<addr>",…]}): per stack,
// the destination move-ins (which plan as creates) AND the source move-outs (which
// plan as destroys). It is the shared core of the `state moves-manifest` CLI and
// the run plan/apply classify pass (renderClassification), which feeds it to
// classification so both sides of a cross-state move classify as 🚚 (non-iam).
// want == "" collects all pending moves (matching applyPendingMoves, which applies
// every pending xmove regardless of PR); a non-empty want filters to that key.
func collectStateMoves(dir, want string) (map[string][]string, error) {
	byStack := map[string]map[string]bool{}
	add := func(stack, addr string) {
		if stack == "" || addr == "" {
			return
		}
		if byStack[stack] == nil {
			byStack[stack] = map[string]bool{}
		}
		byStack[stack][addr] = true
	}

	shims, err := statemove.Discover(dir)
	if err != nil {
		return nil, err
	}
	for _, s := range shims {
		if want != "" && s.Key != want {
			continue
		}
		for _, o := range s.Ops {
			switch o.Kind {
			case "removed": // source move-out → planned destroy
				add(s.Stack, o.From)
			case "import": // dest move-in → planned create
				add(s.Stack, o.To)
			case "moved": // same-stack move (defensive)
				add(s.Stack, o.To)
			}
		}
	}

	xmoves, err := statemove.DiscoverXMoves(dir)
	if err != nil {
		return nil, err
	}
	for _, fx := range xmoves {
		if want != "" && fx.Key != want {
			continue
		}
		// source_stack in the xmove file may be relative to the --dir used when
		// generating it (e.g. "service-projects/fh-dev-svc" written by
		// `state move --via mv --dir stacks/nonprod`). plandir.Scan returns full
		// paths relative to root (e.g. "stacks/nonprod/service-projects/fh-dev-svc").
		// Resolve by walking DestStack's ancestor prefixes until the candidate
		// exists as a directory under dir.
		resolvedSource := resolveSourceStack(dir, fx.DestStack, fx.XMove.SourceStack)
		for _, p := range fx.XMove.Pairs {
			add(resolvedSource, p.From) // source move-out → destroy
			add(fx.DestStack, p.To)     // dest move-in → create
		}
	}

	manifest := make(map[string][]string, len(byStack))
	for stack, set := range byStack {
		addrs := make([]string, 0, len(set))
		for a := range set {
			addrs = append(addrs, a)
		}
		sort.Strings(addrs)
		manifest[stack] = addrs
	}
	return manifest, nil
}
