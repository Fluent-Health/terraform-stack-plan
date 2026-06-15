package reconcile

import (
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

func TestRunnerInitSeedsExecutionAndNotClassified(t *testing.T) {
	got, _ := Step(World{Prior: ChangeSet{PR: 7, Environment: "staging"}},
		RunnerInit{Exec: Execution{ID: "e1", Repo: "r", SHA: "abc", Phase: events.PhasePlanning,
			Stacks: []Stack{{Path: "s1", RunStatus: events.StatusPending}}}})
	if got.Exec.ID != "e1" || len(got.Exec.Stacks) != 1 {
		t.Fatalf("exec not seeded: %+v", got.Exec)
	}
	if _, ok := got.Gate.(NotClassified); !ok {
		t.Fatalf("want NotClassified before finalize, got %T", got.Gate)
	}
}

func TestRunnerPhaseUpdatesPhaseOnly(t *testing.T) {
	prior := ChangeSet{Exec: Execution{ID: "e1", Phase: events.PhasePlanning}}
	got, _ := Step(World{Prior: prior}, RunnerPhase{Phase: events.PhaseApplying})
	if got.Exec.Phase != events.PhaseApplying {
		t.Fatalf("want applying, got %q", got.Exec.Phase)
	}
}

func TestRunnerUpdateSetsStackRunStatus(t *testing.T) {
	prior := ChangeSet{Exec: Execution{Stacks: []Stack{{Path: "s1", RunStatus: events.StatusPending}}}}
	got, _ := Step(World{Prior: prior}, RunnerUpdate{Stack: "s1", Status: events.StatusRunning, Detail: ""})
	if got.Exec.Stacks[0].RunStatus != events.StatusRunning {
		t.Fatalf("want running, got %q", got.Exec.Stacks[0].RunStatus)
	}
}

func TestRunnerUpdateEmitsRenderAndSSE(t *testing.T) {
	prior := ChangeSet{Exec: Execution{Stacks: []Stack{{Path: "s1"}}}}
	_, actions := Step(World{Prior: prior}, RunnerUpdate{Stack: "s1", Status: events.StatusPlanned})
	if !hasAction[RenderCheckRun](actions) || !hasAction[PublishSSE](actions) {
		t.Fatalf("want Render + SSE, got %v", actions)
	}
}
