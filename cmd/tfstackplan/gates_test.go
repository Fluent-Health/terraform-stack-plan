package main

import (
	"sort"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

func TestGatesFromSidecar(t *testing.T) {
	sidecar := []byte(`{
	  "stacks": {
	    "stacks/a": {"categories": [{"category":"iam","icon":"🔐","attributes":{"project":["proj-a"]}}]},
	    "stacks/c": {"categories": [{"category":"move","icon":"🚚"}]}
	  },
	  "summary": {"categories": [
	    {"category":"iam","icon":"🔐","attributes":{"project":["proj-a","proj-b"]}},
	    {"category":"destructive","icon":"💣"}
	  ]}
	}`)
	gating := map[string]bool{"iam": true}

	gates, moving, err := gatesFromSidecar(sidecar, gating, map[string]bool{"iam": true})
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(gates, func(i, j int) bool { return gates[i].Target < gates[j].Target })
	want := []events.GateTarget{{Class: "iam", Target: "proj-a"}, {Class: "iam", Target: "proj-b"}}
	if len(gates) != 2 || gates[0] != want[0] || gates[1] != want[1] {
		t.Fatalf("gates = %+v, want %+v", gates, want)
	}
	if len(moving) != 1 || moving[0] != "stacks/c" {
		t.Fatalf("moving = %+v, want [stacks/c]", moving)
	}
}

func TestGatesFromSidecarNoGatingClasses(t *testing.T) {
	sidecar := []byte(`{"stacks":{},"summary":{"categories":[{"category":"iam","attributes":{"project":["p"]}}]}}`)
	gates, _, err := gatesFromSidecar(sidecar, map[string]bool{}, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if len(gates) != 0 {
		t.Errorf("no gating classes → %+v, want none", gates)
	}
}

func TestGatesFromSidecarEmpty(t *testing.T) {
	gates, moving, err := gatesFromSidecar([]byte(`{"stacks":{},"summary":{"categories":[]}}`), map[string]bool{"iam": true}, map[string]bool{"iam": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(gates) != 0 || len(moving) != 0 {
		t.Errorf("empty → gates %+v moving %+v", gates, moving)
	}
}

// A gating class that declares emit_attributes and matched a change (present in
// the summary) but resolved NO target must fail the pass closed — never emit
// zero gates and let the privileged change (e.g. a destroy whose project the
// derive could not recover) apply unconstrained.
func TestGatesFromSidecarFailsClosedOnUnresolvedTarget(t *testing.T) {
	// iam matched (in summary) but carries no resolved attribute values.
	sidecar := []byte(`{
	  "stacks": {"stacks/a": {"categories": [{"category":"iam","icon":"🔐"}]}},
	  "summary": {"categories": [{"category":"iam","icon":"🔐"}]}
	}`)
	gates, _, err := gatesFromSidecar(sidecar, map[string]bool{"iam": true}, map[string]bool{"iam": true})
	if err == nil {
		t.Fatalf("expected a fail-closed error; got gates=%+v, nil error", gates)
	}
}

// A gating class present in the summary with zero targets but NOT declaring
// emit_attributes (not in requireTargets) does not error — only emit-bearing
// gates must resolve a target.
func TestGatesFromSidecarZeroTargetsNonEmitterOK(t *testing.T) {
	sidecar := []byte(`{"stacks":{},"summary":{"categories":[{"category":"iam","icon":"🔐"}]}}`)
	if _, _, err := gatesFromSidecar(sidecar, map[string]bool{"iam": true}, map[string]bool{}); err != nil {
		t.Fatalf("non-emitter gating class with zero targets must not error: %v", err)
	}
}

func TestCountsFromSidecar(t *testing.T) {
	data := []byte(`{"stacks":{"a":{"categories":[],"counts":{"add":6,"change":2}},` +
		`"b":{"categories":[],"counts":{"destroy":2}}},"summary":{"categories":[]}}`)
	got, err := countsFromSidecar(data)
	if err != nil {
		t.Fatal(err)
	}
	if got["a"].Add != 6 || got["a"].Change != 2 || got["b"].Destroy != 2 {
		t.Fatalf("unexpected counts: %+v", got)
	}
}

func TestCategoriesFromSidecar(t *testing.T) {
	data := []byte(`{"stacks":{
		"stacks/a":{"categories":[{"category":"iam","icon":"🔐"},{"category":"destructive","icon":"💣"}]},
		"stacks/b":{"categories":[{"category":"safe","icon":null}]}
	}}`)
	got, err := categoriesFromSidecar(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(got["stacks/a"]) != 2 || got["stacks/a"][0].Name != "iam" || got["stacks/a"][0].Icon != "🔐" {
		t.Errorf("stacks/a = %+v", got["stacks/a"])
	}
	if len(got["stacks/b"]) != 1 || got["stacks/b"][0].Icon != "" {
		t.Errorf("stacks/b (null icon) = %+v", got["stacks/b"])
	}
}
