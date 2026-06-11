package server

import (
	"regexp"
	"sort"
	"strings"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

// groupKey is a stack's group. When re is non-nil it takes precedence: the
// group is re's first capture group (or whole match if it has no groups);
// paths that don't match are their own group. Otherwise the group is the first
// `depth` slash-separated path segments joined with "/" (depth<=0 or fewer
// segments → the path is its own group).
func groupKey(path string, depth int, re *regexp.Regexp) string {
	if re != nil {
		if m := re.FindStringSubmatch(path); m != nil {
			if len(m) > 1 {
				return m[1]
			}
			return m[0]
		}
		return path
	}
	if depth <= 0 {
		return path
	}
	parts := strings.Split(path, "/")
	if len(parts) <= depth {
		return path
	}
	return strings.Join(parts[:depth], "/")
}

// statusRank orders statuses worst-first for aggregation (higher = worse).
func statusRank(s events.Status) int {
	switch s {
	case events.StatusFailed:
		return 5
	case events.StatusGated:
		return 4
	case events.StatusMoving:
		return 3
	case events.StatusRunning:
		return 2
	case events.StatusPlanned, events.StatusSafe:
		return 1
	default:
		return 0
	}
}

// worstStatus returns the worse (higher-rank) of two statuses.
func worstStatus(a, b events.Status) events.Status {
	if statusRank(b) > statusRank(a) {
		return b
	}
	return a
}

// catCount is a category's occurrence count within a group.
type catCount struct {
	Name  string
	Icon  string
	Count int
}

// groupNode is one group in the group graph.
type groupNode struct {
	Key    string
	Count  int
	Failed int
	Gated  int
	Status events.Status // worst status across the group's stacks
	Cats   []catCount    // category counts within the group, name-sorted
}

// groupGraph is the stacks folded to group nodes + group-level dependency edges.
type groupGraph struct {
	Nodes []groupNode   // sorted by Key
	Edges []events.Edge // group-key edges, deduped, self-edges dropped, sorted
}

// buildGroupGraph folds an execution graph into group nodes (by groupKey at the
// given depth) and aggregates the stack edges to the group level (cross-group
// edges become group edges; intra-group edges dropped; duplicates collapsed).
func buildGroupGraph(g events.Graph, depth int, re *regexp.Regexp) groupGraph {
	acc := map[string]*groupNode{}
	cats := map[string]map[string]*catCount{} // groupKey → name → catCount
	for _, s := range g.Stacks {
		k := groupKey(s.Path, depth, re)
		n := acc[k]
		if n == nil {
			n = &groupNode{Key: k}
			acc[k] = n
		}
		n.Count++
		n.Status = worstStatus(n.Status, s.Status)
		switch s.Status {
		case events.StatusFailed:
			n.Failed++
		case events.StatusGated:
			n.Gated++
		}
		if cats[k] == nil {
			cats[k] = map[string]*catCount{}
		}
		for _, c := range s.Categories {
			cc := cats[k][c.Name]
			if cc == nil {
				cc = &catCount{Name: c.Name, Icon: c.Icon}
				cats[k][c.Name] = cc
			}
			cc.Count++
		}
	}
	keyOf := map[string]string{}
	for _, s := range g.Stacks {
		keyOf[s.Path] = groupKey(s.Path, depth, re)
	}
	edgeSet := map[events.Edge]bool{}
	for _, e := range g.Edges {
		from, to := keyOf[e.From], keyOf[e.To]
		if from == "" || to == "" || from == to {
			continue
		}
		edgeSet[events.Edge{From: from, To: to}] = true
	}

	var gg groupGraph
	for _, n := range acc {
		node := *n
		for _, cc := range cats[n.Key] {
			node.Cats = append(node.Cats, *cc)
		}
		sort.Slice(node.Cats, func(i, j int) bool { return node.Cats[i].Name < node.Cats[j].Name })
		gg.Nodes = append(gg.Nodes, node)
	}
	sort.Slice(gg.Nodes, func(i, j int) bool { return gg.Nodes[i].Key < gg.Nodes[j].Key })
	for e := range edgeSet {
		gg.Edges = append(gg.Edges, e)
	}
	sort.Slice(gg.Edges, func(i, j int) bool {
		if gg.Edges[i].From != gg.Edges[j].From {
			return gg.Edges[i].From < gg.Edges[j].From
		}
		return gg.Edges[i].To < gg.Edges[j].To
	})
	return gg
}
