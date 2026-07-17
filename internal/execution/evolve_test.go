package execution

import (
	"reflect"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

func TestEvolveStartedReplacesState(t *testing.T) {
	prior := State{ID: "old", Phase: events.PhaseApplying}
	got := Evolve(prior, Started{Exec: State{ID: "e1", Stacks: []Stack{{Path: "a"}}}})
	if got.ID != "e1" || len(got.Stacks) != 1 || got.Phase != "" {
		t.Fatalf("Started must replace whole state, got %#v", got)
	}
}

func TestEvolvePhaseChanged(t *testing.T) {
	got := Evolve(State{ID: "e1"}, PhaseChanged{Phase: events.PhaseApplying})
	if got.Phase != events.PhaseApplying || got.ID != "e1" {
		t.Fatalf("want phase applying, id kept; got %#v", got)
	}
}

func TestEvolveStackStatusChanged(t *testing.T) {
	prior := State{Stacks: []Stack{{Path: "a"}, {Path: "b"}}}
	got := Evolve(prior, StackStatusChanged{Stack: "b", Status: events.StatusFailed, Detail: "boom"})
	if got.Stacks[0].RunStatus != "" {
		t.Fatalf("stack a must be untouched, got %q", got.Stacks[0].RunStatus)
	}
	if got.Stacks[1].RunStatus != events.StatusFailed || got.Stacks[1].Detail != "boom" {
		t.Fatalf("stack b not updated: %#v", got.Stacks[1])
	}
}

func TestEvolveFailedAbortsInnocentStacks(t *testing.T) {
	prior := State{Stacks: []Stack{
		{Path: "pend", RunStatus: events.StatusPending},
		{Path: "run", RunStatus: events.StatusRunning},
		{Path: "init", RunStatus: events.StatusInitializing},
		{Path: "inited", RunStatus: events.StatusInitialized},
		{Path: "moving", RunStatus: events.StatusMoving},
		{Path: "done", RunStatus: events.StatusPlanned},
		{Path: "bad", RunStatus: events.StatusFailed},
	}}
	got := Evolve(prior, Failed{})
	want := map[string]events.Status{
		"pend": events.StatusAborted, "run": events.StatusAborted,
		"init": events.StatusAborted, "inited": events.StatusAborted,
		"moving": events.StatusAborted, // moving is innocent → aborted (live behavior)
		"done":   events.StatusPlanned, // terminal statuses are untouched
		"bad":    events.StatusFailed,
	}
	for _, s := range got.Stacks {
		if s.RunStatus != want[s.Path] {
			t.Fatalf("stack %q: want %q, got %q", s.Path, want[s.Path], s.RunStatus)
		}
	}
}

func TestEvolveFoldSequence(t *testing.T) {
	var s State
	for _, e := range []Event{
		Started{Exec: State{ID: "e1", Stacks: []Stack{{Path: "a"}}}},
		PhaseChanged{Phase: events.PhaseApplying},
		StackStatusChanged{Stack: "a", Status: events.StatusRunning},
		Failed{},
	} {
		s = Evolve(s, e)
	}
	want := State{
		ID:     "e1",
		Phase:  events.PhaseApplying,
		Stacks: []Stack{{Path: "a", RunStatus: events.StatusAborted}},
	}
	if !reflect.DeepEqual(s, want) {
		t.Fatalf("fold mismatch:\n got  %#v\n want %#v", s, want)
	}
}
