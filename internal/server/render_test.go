package server

import (
	"strings"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

func TestRenderProgress(t *testing.T) {
	g := events.Graph{Stacks: []events.StackState{
		{Path: "stacks/a", Status: events.StatusPlanned},
		{Path: "stacks/b", Status: events.StatusRunning},
		{Path: "stacks/c", Status: events.StatusPending},
	}}
	out := renderProgress(g)
	if !strings.Contains(out, "1/3") {
		t.Errorf("want done/total count 1/3 in:\n%s", out)
	}
	for _, p := range []string{"stacks/a", "stacks/b", "stacks/c"} {
		if !strings.Contains(out, p) {
			t.Errorf("missing %q in:\n%s", p, out)
		}
	}
	if !strings.Contains(out, "[x]") || !strings.Contains(out, "[ ]") {
		t.Errorf("want both checked and unchecked rows in:\n%s", out)
	}
}

func TestFailuresSection(t *testing.T) {
	none := events.Graph{Stacks: []events.StackState{{Path: "a", Status: events.StatusPlanned}}}
	if failuresSection(none, "") != "" {
		t.Error("no failures should render empty")
	}
	g := events.Graph{Stacks: []events.StackState{{Path: "stacks/x", Status: events.StatusFailed, Detail: "boom"}}}
	out := failuresSection(g, "https://ci/log")
	for _, want := range []string{"Failures (1)", "stacks/x", "boom", "https://ci/log"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}
