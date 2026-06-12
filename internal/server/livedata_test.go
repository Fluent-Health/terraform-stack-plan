package server

import (
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

func TestGroupStacksByKey(t *testing.T) {
	stacks := []events.StackState{
		{Path: "nonprod/pipelines/x"},
		{Path: "nonprod/projects/a"},
		{Path: "nonprod/pipelines/y"},
	}
	groups := groupStacksByKey(stacks, 2, nil)
	if len(groups) != 2 ||
		groups[0].Name != "nonprod/pipelines" || len(groups[0].Stacks) != 2 ||
		groups[1].Name != "nonprod/projects" {
		t.Fatalf("groups = %+v, want nonprod/pipelines(2), nonprod/projects(1)", groups)
	}
}

func TestStatusBadge(t *testing.T) {
	cases := map[events.Status]string{
		events.StatusPending: "badge-ghost",
		events.StatusPlanned: "badge-info",
		events.StatusGated:   "badge-warning",
		events.StatusSafe:    "badge-success",
		events.StatusMoving:  "badge-info",
		events.StatusFailed:  "badge-error",
	}
	for s, want := range cases {
		if got := statusBadge(s); got != want {
			t.Errorf("statusBadge(%q) = %q, want %q", s, got, want)
		}
	}
	if got := statusBadge(events.Status("weird")); got != "badge-ghost" {
		t.Errorf("unknown status badge = %q, want badge-ghost", got)
	}
}

func TestPhaseTimeline(t *testing.T) {
	t.Run("plan in-progress", func(t *testing.T) {
		steps := phaseTimeline("plan", events.PhasePlanning, false)
		if len(steps) != 2 {
			t.Fatalf("plan timeline: got %d steps, want 2", len(steps))
		}
		if steps[0].Name != "Plan" || steps[0].State != "active" {
			t.Errorf("plan step 0: got {%q, %q}, want {Plan, active}", steps[0].Name, steps[0].State)
		}
		if steps[1].Name != "Report" || steps[1].State != "todo" {
			t.Errorf("plan step 1: got {%q, %q}, want {Report, todo}", steps[1].Name, steps[1].State)
		}
	})

	t.Run("plan finished", func(t *testing.T) {
		steps := phaseTimeline("plan", events.PhasePlanning, true)
		for _, st := range steps {
			if st.State != "done" {
				t.Errorf("finished plan: step %q = %q, want done", st.Name, st.State)
			}
		}
	})

	t.Run("apply in-progress", func(t *testing.T) {
		steps := phaseTimeline("apply", events.PhaseApplying, false)
		if len(steps) != 2 {
			t.Fatalf("apply timeline: got %d steps, want 2", len(steps))
		}
		if steps[0].Name != "Apply" || steps[0].State != "active" {
			t.Errorf("apply step 0: got {%q, %q}, want {Apply, active}", steps[0].Name, steps[0].State)
		}
		if steps[1].Name != "Verify" || steps[1].State != "todo" {
			t.Errorf("apply step 1: got {%q, %q}, want {Verify, todo}", steps[1].Name, steps[1].State)
		}
	})

	t.Run("apply finished", func(t *testing.T) {
		steps := phaseTimeline("apply", events.PhaseVerifying, true)
		for _, st := range steps {
			if st.State != "done" {
				t.Errorf("finished apply: step %q = %q, want done", st.Name, st.State)
			}
		}
	})

	t.Run("unknown phase is all todo", func(t *testing.T) {
		for _, st := range phaseTimeline("plan", events.Phase(""), false) {
			if st.State != "todo" {
				t.Errorf("empty-phase step %s = %q, want todo", st.Name, st.State)
			}
		}
	})
}
