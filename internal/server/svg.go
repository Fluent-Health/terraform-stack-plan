package server

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

// statusColor maps a stack status to a colour, using GitHub's light-theme status
// hues so the SVG reads continuously with the check run. Neutral — no brand.
func statusColor(s events.Status) string {
	switch s {
	case events.StatusPlanned, events.StatusSafe:
		return "#1a7f37" // green
	case events.StatusRunning:
		return "#0969da" // blue
	case events.StatusGated:
		return "#9a6700" // amber
	case events.StatusMoving:
		return "#8250df" // purple
	case events.StatusFailed:
		return "#cf222e" // red
	default:
		return "#6e7781" // grey (pending)
	}
}

// layersOf assigns each id a column index = its longest dependency depth from a
// root (a node with no in-edges), via Kahn's algorithm. Deterministic; any node
// left unranked (an unexpected cycle) defaults to layer 0.
func layersOf(ids []string, edges []events.Edge) map[string]int {
	nodes := map[string]bool{}
	indeg := map[string]int{}
	for _, id := range ids {
		nodes[id] = true
		indeg[id] = 0
	}
	adj := map[string][]string{}
	for _, e := range edges {
		if !nodes[e.From] || !nodes[e.To] {
			continue
		}
		adj[e.From] = append(adj[e.From], e.To)
		indeg[e.To]++
	}
	layer := map[string]int{}
	var q []string
	for n := range nodes {
		if indeg[n] == 0 {
			q = append(q, n)
			layer[n] = 0
		}
	}
	sort.Strings(q)
	for len(q) > 0 {
		n := q[0]
		q = q[1:]
		dst := append([]string(nil), adj[n]...)
		sort.Strings(dst)
		for _, m := range dst {
			if layer[n]+1 > layer[m] {
				layer[m] = layer[n] + 1
			}
			indeg[m]--
			if indeg[m] == 0 {
				q = append(q, m)
				sort.Strings(q)
			}
		}
	}
	for n := range nodes {
		if _, ok := layer[n]; !ok {
			layer[n] = 0
		}
	}
	return layer
}

// shortLabel is the stack's display label: its base name, truncated to fit a box.
func shortLabel(path string) string {
	label := path
	if i := strings.LastIndexByte(path, '/'); i >= 0 && i+1 < len(path) {
		label = path[i+1:]
	}
	const max = 22
	if r := []rune(label); len(r) > max {
		label = string(r[:max-1]) + "…"
	}
	return label
}

// svgEscape escapes text for inclusion in SVG/XML.
func svgEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}

// renderSVG renders the execution graph as a self-contained, inert SVG: nodes in
// dependency layers (columns left→right), coloured by status, with straight
// dependency edges. No <script>/<foreignObject>, so it survives GitHub's image
// proxy. Output is deterministic (stable node/edge ordering).
func renderSVG(g events.Graph) []byte {
	const (
		boxW, boxH       = 180, 44
		colGap, rowGap   = 80, 20
		marginX, marginY = 24, 24
	)
	ids := make([]string, 0, len(g.Stacks))
	for _, s := range g.Stacks {
		ids = append(ids, s.Path)
	}
	layer := layersOf(ids, g.Edges)
	byLayer := map[int][]string{}
	maxLayer := 0
	for _, s := range g.Stacks {
		l := layer[s.Path]
		byLayer[l] = append(byLayer[l], s.Path)
		if l > maxLayer {
			maxLayer = l
		}
	}
	for l := range byLayer {
		sort.Strings(byLayer[l])
	}
	type pt struct{ x, y int }
	at := map[string]pt{}
	maxRows := 0
	for l := 0; l <= maxLayer; l++ {
		col := byLayer[l]
		if len(col) > maxRows {
			maxRows = len(col)
		}
		for i, p := range col {
			at[p] = pt{x: marginX + l*(boxW+colGap), y: marginY + i*(boxH+rowGap)}
		}
	}
	width := marginX*2 + (maxLayer+1)*boxW + maxLayer*colGap
	height := marginY*2 + boxH
	if maxRows > 0 {
		height = marginY*2 + maxRows*boxH + (maxRows-1)*rowGap
	}

	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" width="%d" height="%d" font-family="ui-monospace,Menlo,monospace" font-size="12">`,
		width, height, width, height)
	edges := append([]events.Edge(nil), g.Edges...)
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].From != edges[j].From {
			return edges[i].From < edges[j].From
		}
		return edges[i].To < edges[j].To
	})
	for _, e := range edges {
		from, ok1 := at[e.From]
		to, ok2 := at[e.To]
		if !ok1 || !ok2 {
			continue
		}
		fmt.Fprintf(&b, `<line x1="%d" y1="%d" x2="%d" y2="%d" stroke="#d0d7de" stroke-width="2"/>`,
			from.x+boxW, from.y+boxH/2, to.x, to.y+boxH/2)
	}
	for _, s := range g.Stacks {
		p := at[s.Path]
		color := statusColor(s.Status)
		fmt.Fprintf(&b, `<g transform="translate(%d,%d)">`, p.x, p.y)
		fmt.Fprintf(&b, `<rect width="%d" height="%d" rx="8" fill="#ffffff" stroke="%s" stroke-width="2"/>`, boxW, boxH, color)
		fmt.Fprintf(&b, `<rect width="6" height="%d" rx="3" fill="%s"/>`, boxH, color)
		fmt.Fprintf(&b, `<text x="14" y="%d" fill="#1f2328">%s</text>`, boxH/2+4, svgEscape(shortLabel(s.Path)))
		b.WriteString(`</g>`)
	}
	b.WriteString(`</svg>`)
	return []byte(b.String())
}

// groupSummary is the box's second line: stack count + the worst-status tally.
func groupSummary(n groupNode) string {
	s := fmt.Sprintf("%d stacks", n.Count)
	switch {
	case n.Failed > 0:
		s += fmt.Sprintf(" · %d failed", n.Failed)
	case n.Gated > 0:
		s += fmt.Sprintf(" · %d gated", n.Gated)
	}
	return s
}

// clip truncates s to max runes with an ellipsis.
func clip(s string, max int) string {
	if r := []rune(s); len(r) > max {
		return string(r[:max-1]) + "…"
	}
	return s
}

// renderGroupSVG renders the group-level dependency DAG: one box per group
// (count + worst-status), edges = before/after folded to the group level. Inert/
// self-contained like renderSVG. depth<=0 groups by full path (per-stack).
func renderGroupSVG(g events.Graph, depth int) []byte {
	gg := buildGroupGraph(g, depth)
	const (
		boxW, boxH       = 200, 50
		colGap, rowGap   = 80, 20
		marginX, marginY = 24, 24
	)
	ids := make([]string, 0, len(gg.Nodes))
	for _, n := range gg.Nodes {
		ids = append(ids, n.Key)
	}
	layer := layersOf(ids, gg.Edges)
	byLayer := map[int][]string{}
	maxLayer := 0
	for _, n := range gg.Nodes {
		l := layer[n.Key]
		byLayer[l] = append(byLayer[l], n.Key)
		if l > maxLayer {
			maxLayer = l
		}
	}
	for l := range byLayer {
		sort.Strings(byLayer[l])
	}
	type pt struct{ x, y int }
	at := map[string]pt{}
	maxRows := 0
	for l := 0; l <= maxLayer; l++ {
		col := byLayer[l]
		if len(col) > maxRows {
			maxRows = len(col)
		}
		for i, k := range col {
			at[k] = pt{x: marginX + l*(boxW+colGap), y: marginY + i*(boxH+rowGap)}
		}
	}
	width := marginX*2 + (maxLayer+1)*boxW + maxLayer*colGap
	height := marginY*2 + boxH
	if maxRows > 0 {
		height = marginY*2 + maxRows*boxH + (maxRows-1)*rowGap
	}

	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" width="%d" height="%d" font-family="ui-monospace,Menlo,monospace" font-size="12">`,
		width, height, width, height)
	for _, e := range gg.Edges {
		from, ok1 := at[e.From]
		to, ok2 := at[e.To]
		if !ok1 || !ok2 {
			continue
		}
		fmt.Fprintf(&b, `<line x1="%d" y1="%d" x2="%d" y2="%d" stroke="#d0d7de" stroke-width="2"/>`,
			from.x+boxW, from.y+boxH/2, to.x, to.y+boxH/2)
	}
	for _, n := range gg.Nodes {
		p := at[n.Key]
		color := statusColor(n.Status)
		fmt.Fprintf(&b, `<g transform="translate(%d,%d)">`, p.x, p.y)
		fmt.Fprintf(&b, `<rect width="%d" height="%d" rx="8" fill="#ffffff" stroke="%s" stroke-width="2"/>`, boxW, boxH, color)
		fmt.Fprintf(&b, `<rect width="6" height="%d" rx="3" fill="%s"/>`, boxH, color)
		fmt.Fprintf(&b, `<text x="14" y="20" fill="#1f2328">%s</text>`, svgEscape(clip(n.Key, 24)))
		fmt.Fprintf(&b, `<text x="14" y="38" fill="#6e7781" font-size="11">%s</text>`, svgEscape(groupSummary(n)))
		b.WriteString(`</g>`)
	}
	b.WriteString(`</svg>`)
	return []byte(b.String())
}
