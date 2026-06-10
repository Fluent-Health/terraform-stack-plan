package runner

import (
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

func TestParseRunGraph(t *testing.T) {
	dot := `digraph  {
	n1[label="/stacks/a"];
	n2[label="/stacks/b"];
	n3[label="/stacks/c"];
	n1->n2;
	n1->n3;
}`
	got := parseRunGraph(dot)
	want := []events.Edge{
		{From: "stacks/a", To: "stacks/b"},
		{From: "stacks/a", To: "stacks/c"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d edges, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("edge %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestParseRunGraphEmpty(t *testing.T) {
	if got := parseRunGraph("digraph {\n}"); len(got) != 0 {
		t.Errorf("empty graph → %+v, want no edges", got)
	}
}

func TestParseRunGraphStripsLeadingSlash(t *testing.T) {
	got := parseRunGraph(`digraph { n1[label="/a"]; n2[label="/b"]; n1->n2; }`)
	if len(got) != 1 || got[0] != (events.Edge{From: "a", To: "b"}) {
		t.Fatalf("got %+v", got)
	}
}
