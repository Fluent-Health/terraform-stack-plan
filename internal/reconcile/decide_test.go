package reconcile

import (
	"reflect"
	"testing"

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
