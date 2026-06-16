package server

import (
	"strings"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
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
	if failuresSection(none, "", "") != "" {
		t.Error("no failures should render empty")
	}
	g := events.Graph{Stacks: []events.StackState{{Path: "stacks/x", Status: events.StatusFailed, Detail: "boom"}}}
	out := failuresSection(g, "https://ci/log", "")
	for _, want := range []string{"Failures (1)", "stacks/x", "boom", "https://ci/log"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// TestFailuresSectionPerStackLogLink asserts that when a per-stack log prefix is
// given (apply context), each failing stack gets a deep-link to its own streamed
// log at <prefix>/<stack>, and the failure detail (init vs apply phase) renders.
func TestFailuresSectionPerStackLogLink(t *testing.T) {
	g := events.Graph{Stacks: []events.StackState{
		{Path: "cluster/fh-prod", Status: events.StatusFailed, Detail: "terraform apply failed"},
	}}
	out := failuresSection(g, "https://ci/log", "https://serve/logs/apply-1")
	for _, want := range []string{
		"cluster/fh-prod",
		"terraform apply failed",
		"https://serve/logs/apply-1/cluster/fh-prod",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestGatesSection(t *testing.T) {
	if gatesSection(nil) != "" {
		t.Error("no targets → no banner")
	}
	if s := gatesSection([]store.GateTarget{{Class: "iam", Target: "p", State: "ACTIVE"}}); s != "" {
		t.Errorf("all-active → no banner, got %q", s)
	}
	s := gatesSection([]store.GateTarget{
		{Class: "iam", Target: "fh-dev-svc", State: "AWAITING"},
		{Class: "iam", Target: "fh-stage-svc", State: "ACTIVE"},
	})
	if !strings.Contains(s, "Awaiting approval") || !strings.Contains(s, "fh-dev-svc") || !strings.Contains(s, pamConsoleURL("fh-dev-svc")) {
		t.Errorf("pending gate banner missing content: %q", s)
	}
	if strings.Contains(s, "fh-stage-svc") {
		t.Error("active gate should not appear in the awaiting banner")
	}
}
