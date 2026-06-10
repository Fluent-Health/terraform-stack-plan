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

	gates, moving, err := gatesFromSidecar(sidecar, gating)
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
	gates, _, err := gatesFromSidecar(sidecar, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if len(gates) != 0 {
		t.Errorf("no gating classes → %+v, want none", gates)
	}
}

func TestGatesFromSidecarEmpty(t *testing.T) {
	gates, moving, err := gatesFromSidecar([]byte(`{"stacks":{},"summary":{"categories":[]}}`), map[string]bool{"iam": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(gates) != 0 || len(moving) != 0 {
		t.Errorf("empty → gates %+v moving %+v", gates, moving)
	}
}
