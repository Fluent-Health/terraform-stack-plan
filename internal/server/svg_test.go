package server

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

func sampleGraph() events.Graph {
	return events.Graph{
		Stacks: []events.StackState{
			{Path: "stacks/a", Status: events.StatusPlanned},
			{Path: "stacks/b", Status: events.StatusFailed},
			{Path: "stacks/c", Status: events.StatusGated},
		},
		Edges: []events.Edge{{From: "stacks/a", To: "stacks/b"}, {From: "stacks/a", To: "stacks/c"}},
	}
}

func TestRenderSVGEnvelopeAndContent(t *testing.T) {
	out := string(renderSVG(sampleGraph()))
	if !strings.HasPrefix(out, "<svg ") || !strings.HasSuffix(out, "</svg>") {
		t.Fatalf("not a bare svg document:\n%s", out)
	}
	if !strings.Contains(out, "viewBox=") || !strings.Contains(out, `xmlns="http://www.w3.org/2000/svg"`) {
		t.Error("missing viewBox/xmlns")
	}
	if strings.Contains(out, "<script") || strings.Contains(out, "foreignObject") {
		t.Error("svg must be inert (no script/foreignObject)")
	}
	for _, want := range []string{"a", "b", "c"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing node label %q", want)
		}
	}
	for _, hue := range []string{"#cf222e", "#9a6700", "#1a7f37"} {
		if !strings.Contains(out, hue) {
			t.Errorf("missing status hue %s", hue)
		}
	}
	if n := strings.Count(out, "<line "); n != 2 {
		t.Errorf("want 2 edge lines, got %d", n)
	}
}

func TestRenderSVGDeterministic(t *testing.T) {
	g := sampleGraph()
	if !bytes.Equal(renderSVG(g), renderSVG(g)) {
		t.Error("renderSVG must be byte-identical across runs")
	}
}

func TestRenderSVGEmptyGraph(t *testing.T) {
	out := string(renderSVG(events.Graph{}))
	if !strings.HasPrefix(out, "<svg ") || !strings.HasSuffix(out, "</svg>") {
		t.Errorf("empty graph must still render a valid svg:\n%s", out)
	}
}

func TestRenderSVGTruncatesOnRuneBoundary(t *testing.T) {
	long := "stacks/" + strings.Repeat("αβ", 20) // long, multi-byte
	out := renderSVG(events.Graph{Stacks: []events.StackState{{Path: long}}})
	if !utf8.Valid(out) {
		t.Fatal("renderSVG output must be valid UTF-8 even after truncation")
	}
}

func TestRenderGroupSVG(t *testing.T) {
	g := events.Graph{
		Stacks: []events.StackState{
			{Path: "nonprod/projects/a", Status: events.StatusSafe},
			{Path: "nonprod/pipelines/x", Status: events.StatusGated},
			{Path: "nonprod/pipelines/y", Status: events.StatusPlanned},
		},
		Edges: []events.Edge{{From: "nonprod/projects/a", To: "nonprod/pipelines/x"}},
	}
	svg := string(renderGroupSVG(g, 2, nil))
	for _, want := range []string{"<svg", "nonprod/projects", "nonprod/pipelines", "2 stacks", "1 gated", "</svg>"} {
		if !strings.Contains(svg, want) {
			t.Errorf("group SVG missing %q\n%s", want, svg)
		}
	}
	if n := strings.Count(svg, "<line "); n != 1 {
		t.Errorf("group edges drawn = %d, want 1", n)
	}
}

func TestLayersLongestPath(t *testing.T) {
	g := events.Graph{
		Stacks: []events.StackState{{Path: "a"}, {Path: "b"}, {Path: "c"}},
		Edges:  []events.Edge{{From: "a", To: "b"}, {From: "b", To: "c"}, {From: "a", To: "c"}},
	}
	ids := []string{"a", "b", "c"}
	l := layersOf(ids, g.Edges)
	if l["a"] != 0 || l["b"] != 1 || l["c"] != 2 {
		t.Fatalf("layers = %v, want a:0 b:1 c:2", l)
	}
}

func TestRenderGroupSVGBadges(t *testing.T) {
	g := events.Graph{Stacks: []events.StackState{
		{Path: "p/k/a", Status: events.StatusPlanned, Categories: []events.Category{{Name: "iam", Icon: "🔐"}}},
		{Path: "p/k/b", Status: events.StatusPlanned, Categories: []events.Category{{Name: "iam", Icon: "🔐"}}},
	}}
	svg := string(renderGroupSVG(g, 2, nil))
	if !strings.Contains(svg, "🔐 2") {
		t.Errorf("group SVG missing the 🔐 2 badge:\n%s", svg)
	}
}

func TestRenderGroupSVGSwimlanes(t *testing.T) {
	g := events.Graph{
		Stacks: []events.StackState{
			{Path: "nonprod/projects/a", Status: events.StatusSafe},
			{Path: "nonprod/pipelines/x", Status: events.StatusGated},
			{Path: "prod/pipelines/z", Status: events.StatusFailed},
		},
		Edges: []events.Edge{{From: "nonprod/projects/a", To: "nonprod/pipelines/x"}},
	}
	svg := string(renderGroupSVG(g, 2, nil))
	// lane labels for each environment (the bare first segment, distinct from the
	// box keys "nonprod/pipelines" etc.)
	if !strings.Contains(svg, ">nonprod<") {
		t.Error("missing nonprod lane label")
	}
	if !strings.Contains(svg, ">prod<") {
		t.Error("missing prod lane label")
	}
	// group boxes + the one cross-group edge still render
	if !strings.Contains(svg, "nonprod/pipelines") || !strings.Contains(svg, "prod/pipelines") {
		t.Error("missing group boxes")
	}
	if n := strings.Count(svg, "<line "); n != 1 {
		t.Errorf("edges = %d, want 1", n)
	}
}
