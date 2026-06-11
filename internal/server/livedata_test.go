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
	steps := phaseTimeline(events.PhasePlanning)
	if len(steps) != 5 {
		t.Fatalf("steps = %d, want 5", len(steps))
	}
	want := []string{"done", "done", "active", "todo", "todo"}
	for i, st := range steps {
		if st.State != want[i] {
			t.Errorf("step %d (%s) state = %q, want %q", i, st.Name, st.State, want[i])
		}
	}
	for _, st := range phaseTimeline(events.Phase("")) {
		if st.State != "todo" {
			t.Errorf("empty-phase step %s = %q, want todo", st.Name, st.State)
		}
	}
}
