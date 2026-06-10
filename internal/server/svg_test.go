package server

import (
	"bytes"
	"strings"
	"testing"

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

func TestLayersLongestPath(t *testing.T) {
	g := events.Graph{
		Stacks: []events.StackState{{Path: "a"}, {Path: "b"}, {Path: "c"}},
		Edges:  []events.Edge{{From: "a", To: "b"}, {From: "b", To: "c"}, {From: "a", To: "c"}},
	}
	l := layers(g)
	if l["a"] != 0 || l["b"] != 1 || l["c"] != 2 {
		t.Fatalf("layers = %v, want a:0 b:1 c:2", l)
	}
}
