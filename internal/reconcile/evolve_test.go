package reconcile

import (
	"reflect"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

func TestEvolveExecutionStartedSetsExecAndDefaultsGate(t *testing.T) {
	exec := Execution{ID: "e1", Stacks: []Stack{{Path: "a"}}}
	got := Evolve(ChangeSet{}, ExecutionStarted{Exec: exec})
	if !reflect.DeepEqual(got.Exec, exec) {
		t.Fatalf("exec not folded: %+v", got.Exec)
	}
	if _, ok := got.Gate.(NotClassified); !ok {
		t.Fatalf("gate should default to NotClassified, got %T", got.Gate)
	}
}

func TestEvolveExecutionStartedKeepsExistingGate(t *testing.T) {
	prior := ChangeSet{Gate: Clean{}}
	got := Evolve(prior, ExecutionStarted{Exec: Execution{ID: "e1"}})
	if _, ok := got.Gate.(Clean); !ok {
		t.Fatalf("existing gate must be preserved, got %T", got.Gate)
	}
}

func TestEvolveStackStatusChanged(t *testing.T) {
	prior := ChangeSet{Exec: Execution{Stacks: []Stack{{Path: "a"}, {Path: "b"}}}}
	got := Evolve(prior, StackStatusChanged{Stack: "b", Status: events.StatusFailed, Detail: "boom"})
	if got.Exec.Stacks[1].RunStatus != events.StatusFailed || got.Exec.Stacks[1].Detail != "boom" {
		t.Fatalf("stack b not updated: %+v", got.Exec.Stacks[1])
	}
	if got.Exec.Stacks[0].RunStatus == events.StatusFailed {
		t.Fatalf("stack a wrongly mutated")
	}
}

func TestEvolveGateSatisfied(t *testing.T) {
	prior := ChangeSet{Gate: Pending{Targets: []Target{{Class: "c", Target: "t", Grant: approval.StateActive}}}}
	got := Evolve(prior, GateSatisfied{})
	if _, ok := got.Gate.(Satisfied); !ok {
		t.Fatalf("want Satisfied, got %T", got.Gate)
	}
}

func TestEvolveGateReleasedAndPassedBothClean(t *testing.T) {
	for _, e := range []Event{GatePassed{}, GateReleased{}} {
		got := Evolve(ChangeSet{Gate: Pending{}}, e)
		if _, ok := got.Gate.(Clean); !ok {
			t.Fatalf("%T should fold to Clean, got %T", e, got.Gate)
		}
	}
}

// corpus is one example of every Event variant, for the totality + determinism
// properties. Extend it whenever a new Event is added.
func corpus() []Event {
	return []Event{
		ExecutionStarted{Exec: Execution{Stacks: []Stack{{Path: "a"}}}},
		PhaseChanged{Phase: events.PhaseApplying},
		StackStatusChanged{Stack: "a", Status: events.StatusRunning},
		ExecutionFailed{},
		StacksClassified{Projects: map[string]string{"a": "p"}},
		Classified{Gates: []events.GateTarget{{Class: "c", Target: "t"}}},
		GrantObserved{Class: "c", Target: "t", Name: "g1", State: approval.StateActive, Requester: "sa"},
		GrantCleared{Class: "c", Target: "t"},
		GateTargetRequested{Class: "c", Target: "t"},
		GateSatisfied{},
		GateBlocked{Reason: ReasonDenied},
		TargetRevoked{Class: "c", Target: "t"},
		GatePassed{}, GateReleased{},
		ClaimReleased{PR: 1, Environment: "nonprod"},
		PRClosedRecorded{},
	}
}

func priorStates() []ChangeSet {
	return []ChangeSet{
		{},
		{Gate: NotClassified{}},
		{Gate: Clean{}},
		{Gate: Pending{Targets: []Target{{Class: "c", Target: "t", GrantName: "g1", Grant: approval.StateActive}}, Lease: Lease{Requester: "sa"}}},
		{Gate: Satisfied{Targets: []Target{{Class: "c", Target: "t", Grant: approval.StateActive}}}},
		{Gate: Blocked{Targets: []Target{{Class: "c", Target: "t"}}, By: Blocker{Reason: ReasonDenied}}},
	}
}

func TestEvolveIsTotal(t *testing.T) {
	for _, st := range priorStates() {
		for _, e := range corpus() {
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("Evolve panicked on %T over %T gate: %v", e, st.Gate, r)
					}
				}()
				_ = Evolve(st, e)
			}()
		}
	}
}

func TestEvolveIsDeterministic(t *testing.T) {
	for _, st := range priorStates() {
		a, b := st, st
		for _, e := range corpus() {
			a = Evolve(a, e)
		}
		for _, e := range corpus() {
			b = Evolve(b, e)
		}
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("folding the same events twice diverged for prior %T", st.Gate)
		}
	}
}
