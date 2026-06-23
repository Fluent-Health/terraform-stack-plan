package reconcile

import (
	"reflect"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

func TestDecideRunnerInitEmitsExecutionStarted(t *testing.T) {
	exec := Execution{ID: "e1"}
	got := Decide(ChangeSet{}, RunnerInit{Exec: exec})
	want := []Event{ExecutionStarted{Exec: exec}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}

func TestDecideRunnerPhaseEmitsPhaseChanged(t *testing.T) {
	got := Decide(ChangeSet{}, RunnerPhase{Phase: events.PhaseApplying})
	if len(got) != 1 || got[0] != (PhaseChanged{Phase: events.PhaseApplying}) {
		t.Fatalf("got %#v", got)
	}
}

func TestDecideRunnerUpdateEmitsStackStatusChanged(t *testing.T) {
	got := Decide(ChangeSet{}, RunnerUpdate{Stack: "a", Status: events.StatusRunning, Detail: "d"})
	if len(got) != 1 || got[0] != (StackStatusChanged{Stack: "a", Status: events.StatusRunning, Detail: "d"}) {
		t.Fatalf("got %#v", got)
	}
}

func TestDecideApplySucceededNotClassifiedNoEvents(t *testing.T) {
	if got := Decide(ChangeSet{Gate: NotClassified{}}, ApplySucceeded{}); len(got) != 0 {
		t.Fatalf("want no events, got %#v", got)
	}
}

func TestDecideApplySucceededReleasesClaimAndRevokesGrants(t *testing.T) {
	cs := ChangeSet{PR: 7, Environment: "nonprod", Gate: Satisfied{
		Targets: []Target{{Class: "c", Target: "t", GrantName: "g1", Grant: approval.StateActive}},
	}}
	got := Decide(cs, ApplySucceeded{})
	want := []Event{
		ClaimReleased{PR: 7, Environment: "nonprod"},
		TargetRevoked{Class: "c", Target: "t", PR: 7, Env: "nonprod"},
		GateReleased{},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}
