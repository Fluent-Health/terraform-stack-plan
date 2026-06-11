package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"

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

	shims, err := statemove.Discover(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "state moves-manifest:", err)
		return 1
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

	xmoves, err := statemove.DiscoverXMoves(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "state moves-manifest:", err)
		return 1
	}
	for _, fx := range xmoves {
		if want != "" && fx.Key != want {
			continue
		}
		for _, p := range fx.XMove.Pairs {
			add(fx.XMove.SourceStack, p.From) // source move-out → destroy
			add(fx.DestStack, p.To)           // dest move-in → create
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
