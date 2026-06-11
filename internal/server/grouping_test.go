package server

import (
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

func TestGroupKey(t *testing.T) {
	cases := map[string]string{
		"nonprod/pipelines/content-library": "nonprod/pipelines",
		"prod/projects/fh-prod-svc":         "prod/projects",
		"single":                            "single",
	}
	for path, want := range cases {
		if got := groupKey(path, 2); got != want {
			t.Errorf("groupKey(%q,2) = %q, want %q", path, got, want)
		}
	}
}

func TestBuildGroupGraph(t *testing.T) {
	g := events.Graph{
		Stacks: []events.StackState{
			{Path: "nonprod/projects/a", Status: events.StatusSafe},
			{Path: "nonprod/pipelines/x", Status: events.StatusGated},
			{Path: "nonprod/pipelines/y", Status: events.StatusPlanned},
			{Path: "prod/pipelines/z", Status: events.StatusFailed},
		},
		Edges: []events.Edge{
			{From: "nonprod/projects/a", To: "nonprod/pipelines/x"},
			{From: "nonprod/pipelines/x", To: "nonprod/pipelines/y"}, // intra-group → dropped
		},
	}
	gg := buildGroupGraph(g, 2)
	byKey := map[string]groupNode{}
	for _, n := range gg.Nodes {
		byKey[n.Key] = n
	}
	if len(gg.Nodes) != 3 {
		t.Fatalf("nodes = %d, want 3 (%+v)", len(gg.Nodes), gg.Nodes)
	}
	if n := byKey["nonprod/pipelines"]; n.Count != 2 || n.Status != events.StatusGated {
		t.Errorf("nonprod/pipelines = %+v, want count 2 + worst gated", n)
	}
	if n := byKey["prod/pipelines"]; n.Status != events.StatusFailed {
		t.Errorf("prod/pipelines worst = %v, want failed", n.Status)
	}
	if len(gg.Edges) != 1 || gg.Edges[0] != (events.Edge{From: "nonprod/projects", To: "nonprod/pipelines"}) {
		t.Errorf("edges = %+v, want one nonprod/projects→nonprod/pipelines", gg.Edges)
	}
}

func TestWorstStatus(t *testing.T) {
	if worstStatus(events.StatusGated, events.StatusFailed) != events.StatusFailed {
		t.Error("failed should win over gated")
	}
	if worstStatus(events.StatusSafe, events.StatusGated) != events.StatusGated {
		t.Error("gated should win over safe")
	}
}
