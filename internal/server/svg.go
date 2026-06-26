package server

import (
	"fmt"
	"regexp"
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
	case events.StatusNochange:
		return "#8aab95" // faded green (applied, no changes)
	case events.StatusAborted:
		return "#8c959f" // light grey (run aborted, distinct from pending)
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
		anchor := anchorSlug(s.Path)
		fmt.Fprintf(&b, `<a href="#%s">`, anchor)
		fmt.Fprintf(&b, `<g transform="translate(%d,%d)" id="svg-%s" class="svg-node">`, p.x, p.y, anchor)
		fmt.Fprintf(&b, `<rect class="svg-border" width="%d" height="%d" rx="8" fill="#ffffff" stroke="%s" stroke-width="2"/>`, boxW, boxH, color)
		fmt.Fprintf(&b, `<rect class="svg-bar" width="6" height="%d" rx="3" fill="%s"/>`, boxH, color)
		fmt.Fprintf(&b, `<text x="14" y="%d" fill="#1f2328">%s</text>`, boxH/2+4, svgEscape(shortLabel(s.Path)))
		b.WriteString(`</g>`)
		b.WriteString(`</a>`)
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

// groupBadges is the category-badge line: "🔐 12  💣 5" (icon when present, else
// name), in the group's category order. Empty when the group has no categories.
func groupBadges(n groupNode) string {
	parts := make([]string, 0, len(n.Cats))
	for _, c := range n.Cats {
		label := c.Icon
		if label == "" {
			label = c.Name
		}
		parts = append(parts, fmt.Sprintf("%s %d", label, c.Count))
	}
	return strings.Join(parts, "  ")
}

// clip truncates s to max runes with an ellipsis.
func clip(s string, max int) string {
	if r := []rune(s); len(r) > max {
		return string(r[:max-1]) + "…"
	}
	return s
}

// laneOf is the swimlane a group belongs to: the first segment of its key
// (the environment, e.g. "nonprod/pipelines" → "nonprod").
func laneOf(key string) string {
	if i := strings.IndexByte(key, '/'); i >= 0 {
		return key[:i]
	}
	return key
}

// renderGroupSVG renders the group-level dependency DAG as an inert SVG, laid out
// in horizontal lanes per environment (laneOf the group key) sharing one
// dependency-depth column grid. Each group box shows count + worst-status + the
// category badges.
func renderGroupSVG(g events.Graph, depth int, re *regexp.Regexp) []byte {
	gg := buildGroupGraph(g, depth, re)
	const (
		boxW, boxH       = 200, 64
		colGap, rowGap   = 80, 16
		marginX, marginY = 24, 24
		laneLabelH       = 22
		laneGap          = 24
	)
	ids := make([]string, 0, len(gg.Nodes))
	for _, n := range gg.Nodes {
		ids = append(ids, n.Key)
	}
	layer := layersOf(ids, gg.Edges)
	maxLayer := 0
	for _, l := range layer {
		if l > maxLayer {
			maxLayer = l
		}
	}
	// lanes (environments), sorted.
	laneSet := map[string]bool{}
	for _, n := range gg.Nodes {
		laneSet[laneOf(n.Key)] = true
	}
	lanes := make([]string, 0, len(laneSet))
	for l := range laneSet {
		lanes = append(lanes, l)
	}
	sort.Strings(lanes)

	type pt struct{ x, y int }
	at := map[string]pt{}
	type band struct {
		name string
		top  int
	}
	var bands []band
	y := marginY
	for _, lane := range lanes {
		byLayer := map[int][]string{}
		for _, n := range gg.Nodes {
			if laneOf(n.Key) == lane {
				byLayer[layer[n.Key]] = append(byLayer[layer[n.Key]], n.Key)
			}
		}
		for l := range byLayer {
			sort.Strings(byLayer[l])
		}
		rows := 0
		for _, col := range byLayer {
			if len(col) > rows {
				rows = len(col)
			}
		}
		if rows == 0 {
			rows = 1
		}
		bands = append(bands, band{name: lane, top: y})
		nodeTop := y + laneLabelH
		for l := 0; l <= maxLayer; l++ {
			for i, k := range byLayer[l] {
				at[k] = pt{x: marginX + l*(boxW+colGap), y: nodeTop + i*(boxH+rowGap)}
			}
		}
		y = nodeTop + rows*boxH + (rows-1)*rowGap + laneGap
	}
	width := marginX*2 + (maxLayer+1)*boxW + maxLayer*colGap
	height := y - laneGap + marginY
	if len(gg.Nodes) == 0 {
		height = marginY*2 + boxH
	}

	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" width="%d" height="%d" font-family="ui-monospace,Menlo,monospace" font-size="12">`,
		width, height, width, height)
	for _, bd := range bands {
		fmt.Fprintf(&b, `<text x="%d" y="%d" fill="#57606a" font-weight="bold" font-size="13">%s</text>`, marginX, bd.top+15, svgEscape(bd.name))
	}
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
		if badges := groupBadges(n); badges != "" {
			fmt.Fprintf(&b, `<text x="14" y="54" fill="#1f2328" font-size="12">%s</text>`, svgEscape(badges))
		}
		b.WriteString(`</g>`)
	}
	b.WriteString(`</svg>`)
	return []byte(b.String())
}
