package server

import (
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

func TestGroupStacks(t *testing.T) {
	in := []events.StackState{
		{Path: "stacks/a", Project: "proj-b", Status: events.StatusPlanned},
		{Path: "stacks/b", Project: "proj-a", Status: events.StatusSafe},
		{Path: "stacks/c", Project: "proj-b", Status: events.StatusGated},
		{Path: "stacks/d", Status: events.StatusPending}, // no project
	}
	groups := groupStacks(in)
	if len(groups) != 3 {
		t.Fatalf("groups = %d, want 3", len(groups))
	}
	if groups[0].Name != "proj-a" || groups[1].Name != "proj-b" {
		t.Errorf("group order = %q,%q, want proj-a,proj-b", groups[0].Name, groups[1].Name)
	}
	if groups[2].Name != "(ungrouped)" || len(groups[2].Stacks) != 1 {
		t.Errorf("last group = %q (%d stacks), want (ungrouped) (1)", groups[2].Name, len(groups[2].Stacks))
	}
	if len(groups[1].Stacks) != 2 {
		t.Errorf("proj-b stacks = %d, want 2", len(groups[1].Stacks))
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
