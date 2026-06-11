package server

import (
	"regexp"
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
		if got := groupKey(path, 2, nil); got != want {
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
	gg := buildGroupGraph(g, 2, nil)
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

func TestBuildGroupGraphCategories(t *testing.T) {
	g := events.Graph{Stacks: []events.StackState{
		{Path: "p/k/a", Categories: []events.Category{{Name: "iam", Icon: "🔐"}}},
		{Path: "p/k/b", Categories: []events.Category{{Name: "iam", Icon: "🔐"}, {Name: "destructive", Icon: "💣"}}},
	}}
	gg := buildGroupGraph(g, 2, nil)
	if len(gg.Nodes) != 1 {
		t.Fatalf("nodes = %d, want 1", len(gg.Nodes))
	}
	cc := map[string]int{}
	for _, c := range gg.Nodes[0].Cats {
		cc[c.Name] = c.Count
	}
	if cc["iam"] != 2 || cc["destructive"] != 1 {
		t.Errorf("category counts = %+v, want iam:2 destructive:1", gg.Nodes[0].Cats)
	}
}

func TestGroupKeyPattern(t *testing.T) {
	re := regexp.MustCompile(`^([^/]+/[^/]+)`)
	if got := groupKey("a/b/c/d", 2, re); got != "a/b" {
		t.Errorf("pattern groupKey = %q, want a/b", got)
	}
	re2 := regexp.MustCompile(`^[^/]+`)
	if got := groupKey("a/b/c", 2, re2); got != "a" {
		t.Errorf("whole-match groupKey = %q, want a", got)
	}
	re3 := regexp.MustCompile(`^zzz`)
	if got := groupKey("a/b/c", 2, re3); got != "a/b/c" {
		t.Errorf("no-match groupKey = %q, want a/b/c", got)
	}
	if got := groupKey("a/b/c", 2, nil); got != "a/b" {
		t.Errorf("depth groupKey = %q, want a/b", got)
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
