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
