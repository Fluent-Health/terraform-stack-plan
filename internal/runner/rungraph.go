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
