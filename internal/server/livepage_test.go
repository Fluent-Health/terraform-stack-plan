package server

import (
	"strings"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

func TestApprovalPanelStates(t *testing.T) {
	if approvalPanel(nil) != "" {
		t.Error("no targets → empty panel")
	}
	panel := approvalPanel([]store.GateTarget{
		{Class: "iam", Target: "proj-a", State: "ACTIVE"},
		{Class: "iam", Target: "proj-b", State: "AWAITING"},
		{Class: "database", Target: "proj-c", State: "blocked"},
	})
	for _, want := range []string{"proj-a", "proj-b", "proj-c", "iam", "database", "Approved", "Waiting", "Blocked"} {
		if !strings.Contains(panel, want) {
			t.Errorf("panel missing %q", want)
		}
	}
	if strings.Contains(panel, "console.cloud.google.com") || strings.Contains(panel, "cloud.google") {
		t.Error("approval panel must not hardcode a provider console URL")
	}
}

func TestLivePageRendersShell(t *testing.T) {
	a := New(newServerTestDB(t), &MockGitHub{}, Config{})
	html := a.livePage(liveView{
		Repo: "octo/repo", Environment: "staging", Report: "PLAN_REPORT_BODY",
		SVG: `<svg id="dag"></svg>`, Panel: `<div class="panel">P</div>`,
	})
	for _, want := range []string{
		`/assets/app.css`,      // links the embedded stylesheet
		`data-theme`,           // DaisyUI theme on <html>
		`octo/repo`,            // repo shown
		`staging`,              // environment shown
		`<svg id="dag">`,       // trusted SVG injected un-escaped
		`class="panel"`,        // trusted panel injected un-escaped
		`PLAN_REPORT_BODY`,     // report body present
		`http-equiv="refresh"`, // 10s auto-refresh preserved
	} {
		if !strings.Contains(html, want) {
			t.Errorf("live page missing %q", want)
		}
	}
}

func TestLivePageEscapesReport(t *testing.T) {
	a := New(newServerTestDB(t), &MockGitHub{}, Config{})
	html := a.livePage(liveView{Repo: "r", Report: "<script>evil()</script>"})
	if strings.Contains(html, "<script>evil()</script>") {
		t.Error("report body must be HTML-escaped")
	}
	if !strings.Contains(html, "&lt;script&gt;") {
		t.Error("expected the report to be escaped into the page")
	}
}

func TestLivePageStackListAndTimeline(t *testing.T) {
	a := New(newServerTestDB(t), &MockGitHub{}, Config{})
	stacks := []events.StackState{
		{Path: "stacks/a", Project: "proj-a", Status: events.StatusGated},
		{Path: "stacks/b", Status: events.StatusSafe},
	}
	html := a.livePage(liveView{
		Repo: "o/r", Environment: "staging", Phase: events.PhasePlanning,
		Stacks: stacks, Report: "", SVG: `<svg id="dag"></svg>`, Panel: "",
	})
	for _, want := range []string{
		"proj-a", "(ungrouped)", "stacks/a",
		"badge-warning", "badge-success",
		"steps", "planning",
		`<svg id="dag">`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("live page missing %q", want)
		}
	}
}
