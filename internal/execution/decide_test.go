package execution

import (
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

func TestDecideReportInitEmitsStarted(t *testing.T) {
	exec := State{ID: "e1", Repo: "r", SHA: "abc", Stacks: []Stack{{Path: "a"}}}
	got := Decide(State{}, ReportInit{Exec: exec})
	if len(got) != 1 {
		t.Fatalf("want 1 event, got %d", len(got))
	}
	if s, ok := got[0].(Started); !ok || s.Exec.ID != "e1" {
		t.Fatalf("want Started{e1}, got %#v", got[0])
	}
}

func TestDecideReportPhaseEmitsPhaseChanged(t *testing.T) {
	got := Decide(State{}, ReportPhase{Phase: events.PhaseApplying})
	if len(got) != 1 || got[0] != (PhaseChanged{Phase: events.PhaseApplying}) {
		t.Fatalf("want PhaseChanged{applying}, got %#v", got)
	}
}

func TestDecideReportTickEmitsStackStatusChanged(t *testing.T) {
	got := Decide(State{}, ReportTick{Stack: "a", Status: events.StatusRunning, Detail: "d"})
	want := StackStatusChanged{Stack: "a", Status: events.StatusRunning, Detail: "d"}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("want %#v, got %#v", want, got)
	}
}

func TestDecideReportFailEmitsFailed(t *testing.T) {
	got := Decide(State{}, ReportFail{})
	if len(got) != 1 {
		t.Fatalf("want 1 event, got %d", len(got))
	}
	if _, ok := got[0].(Failed); !ok {
		t.Fatalf("want Failed, got %T", got[0])
	}
}

func TestDecideReportSucceedEmitsSucceeded(t *testing.T) {
	got := Decide(State{}, ReportSucceed{})
	if len(got) != 1 {
		t.Fatalf("want 1 event, got %d", len(got))
	}
	if _, ok := got[0].(Succeeded); !ok {
		t.Fatalf("want Succeeded, got %T", got[0])
	}
}

func TestDecideReportAnnotateEmitsStacksAnnotated(t *testing.T) {
	got := Decide(State{}, ReportAnnotate{Projects: map[string]string{"a": "p"}})
	if len(got) != 1 {
		t.Fatalf("want 1 event, got %d", len(got))
	}
	if _, ok := got[0].(StacksAnnotated); !ok {
		t.Fatalf("want StacksAnnotated, got %T", got[0])
	}
}

func TestDecideReportSupersedeEmitsSuperseded(t *testing.T) {
	got := Decide(State{ID: "e1"}, ReportSupersede{By: "new-exec"})
	if len(got) != 1 {
		t.Fatalf("want 1 event, got %d", len(got))
	}
	if s, ok := got[0].(Superseded); !ok || s.By != "new-exec" {
		t.Fatalf("want Superseded{new-exec}, got %#v", got[0])
	}
}

func TestDecideReportSupersedeNoopOnEmptyState(t *testing.T) {
	if got := Decide(State{}, ReportSupersede{By: "x"}); got != nil {
		t.Fatalf("want nil on empty state, got %#v", got)
	}
	// A materialized execution still emits the fact.
	got := Decide(State{ID: "e1"}, ReportSupersede{By: "x"})
	if len(got) != 1 {
		t.Fatalf("want 1 event for materialized exec, got %d", len(got))
	}
}
