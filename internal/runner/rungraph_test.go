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

// run-graph node labels are project-root-relative (stacks/nonprod/cluster/x)
// while `terramate list` under --dir stacks/nonprod yields tier-relative paths
// (cluster/x). NormalizeEdges must map edge endpoints onto the listed stack
// namespace and drop edges touching stacks outside the run's set — otherwise
// every stored edge dangles and the UI dependency graph renders blank.
func TestNormalizeEdges(t *testing.T) {
	stacks := []string{"cluster/fh-dev-svc", "workloads/agent/fh-dev-svc", "projects/fh-dev-svc"}
	edges := []events.Edge{
		// prefix-qualified endpoints that suffix-match listed stacks → kept, renamed
		{From: "stacks/nonprod/projects/fh-dev-svc", To: "stacks/nonprod/cluster/fh-dev-svc"},
		{From: "stacks/nonprod/cluster/fh-dev-svc", To: "stacks/nonprod/workloads/agent/fh-dev-svc"},
		// endpoint outside the changed set → dropped
		{From: "stacks/nonprod/sql/fh-dev-svc", To: "stacks/nonprod/workloads/agent/fh-dev-svc"},
		// both outside → dropped
		{From: "stacks/nonprod/observability/grafana-cloud/fh-dev-svc", To: "stacks/nonprod/observability/grafana-alloy/fh-dev-svc"},
		// already in the listed namespace → kept as-is
		{From: "projects/fh-dev-svc", To: "workloads/agent/fh-dev-svc"},
	}
	got := NormalizeEdges(stacks, edges)
	want := []events.Edge{
		{From: "projects/fh-dev-svc", To: "cluster/fh-dev-svc"},
		{From: "cluster/fh-dev-svc", To: "workloads/agent/fh-dev-svc"},
		{From: "projects/fh-dev-svc", To: "workloads/agent/fh-dev-svc"},
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

// A listed stack that is a path suffix of a longer, different listed stack must
// only match exactly (no cross-stack suffix capture).
func TestNormalizeEdgesExactBeforeSuffix(t *testing.T) {
	stacks := []string{"agent/fh-dev-svc", "workloads/agent/fh-dev-svc"}
	edges := []events.Edge{{From: "agent/fh-dev-svc", To: "workloads/agent/fh-dev-svc"}}
	got := NormalizeEdges(stacks, edges)
	if len(got) != 1 || got[0] != (events.Edge{From: "agent/fh-dev-svc", To: "workloads/agent/fh-dev-svc"}) {
		t.Errorf("got %+v; exact matches must win over suffix matches", got)
	}
}

func TestNormalizeEdgesNilSafe(t *testing.T) {
	if got := NormalizeEdges(nil, nil); got == nil || len(got) != 0 {
		t.Errorf("got %#v, want non-nil empty slice", got)
	}
}
