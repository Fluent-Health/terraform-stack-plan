package runner

import (
	"regexp"
	"strings"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

var (
	// nodeRE matches a DOT node decl: n1[label="/stacks/a"]
	nodeRE = regexp.MustCompile(`(\w+)\s*\[label="([^"]*)"\]`)
	// edgeRE matches a DOT edge: n1->n2
	edgeRE = regexp.MustCompile(`(\w+)\s*->\s*(\w+)`)
)

// parseRunGraph parses `terramate experimental run-graph -l stack.dir` DOT output
// into dependency edges. Node labels are project-absolute stack dirs
// (/stacks/a); the leading slash is stripped so edges match `terramate list`
// output (stacks/a). Returns a non-nil empty slice when there are no edges.
func parseRunGraph(dot string) []events.Edge {
	label := map[string]string{}
	for _, m := range nodeRE.FindAllStringSubmatch(dot, -1) {
		label[m[1]] = strings.TrimPrefix(m[2], "/")
	}
	edges := []events.Edge{}
	for _, m := range edgeRE.FindAllStringSubmatch(dot, -1) {
		from, fok := label[m[1]]
		to, tok := label[m[2]]
		if fok && tok {
			edges = append(edges, events.Edge{From: from, To: to})
		}
	}
	return edges
}

// NormalizeEdges maps run-graph edge endpoints onto the listed stack namespace
// and drops edges touching stacks outside the set. The two commands live in
// different namespaces: `terramate list` under --dir <tier> yields tier-relative
// paths (cluster/x) while `experimental run-graph` labels nodes project-root-
// relative (stacks/nonprod/cluster/x). An exact match wins; otherwise an
// endpoint matches the unique listed stack it path-suffixes ("…/"+stack).
// Always returns a non-nil slice.
func NormalizeEdges(stacks []string, edges []events.Edge) []events.Edge {
	exact := make(map[string]bool, len(stacks))
	for _, s := range stacks {
		exact[s] = true
	}
	resolve := func(endpoint string) (string, bool) {
		if exact[endpoint] {
			return endpoint, true
		}
		for _, s := range stacks {
			if strings.HasSuffix(endpoint, "/"+s) {
				return s, true
			}
		}
		return "", false
	}
	out := []events.Edge{}
	for _, e := range edges {
		from, fok := resolve(e.From)
		to, tok := resolve(e.To)
		if fok && tok {
			out = append(out, events.Edge{From: from, To: to})
		}
	}
	return out
}
