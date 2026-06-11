package server

import (
	"strings"
	"testing"

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
	html := a.livePage("octo/repo", "staging", "PLAN_REPORT_BODY", `<svg id="dag"></svg>`, `<div class="panel">P</div>`)
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
	html := a.livePage("r", "", "<script>evil()</script>", "", "")
	if strings.Contains(html, "<script>evil()</script>") {
		t.Error("report body must be HTML-escaped")
	}
	if !strings.Contains(html, "&lt;script&gt;") {
		t.Error("expected the report to be escaped into the page")
	}
}
