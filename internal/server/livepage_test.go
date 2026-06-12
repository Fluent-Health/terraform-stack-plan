package server

import (
	"html/template"
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
		`/assets/app.css`,                      // links the embedded stylesheet
		`data-theme`,                           // DaisyUI theme on <html>
		`octo/repo`,                            // repo shown
		`staging`,                              // environment shown
		`<svg id="dag">`,                       // trusted SVG injected un-escaped
		`class="panel"`,                        // trusted panel injected un-escaped
		`PLAN_REPORT_BODY`,                     // report body rendered (goldmark wraps in <p>)
		`new EventSource`,                      // SSE subscription
		`/events`,                              // SSE endpoint path suffix
		`<noscript><meta http-equiv="refresh"`, // meta-refresh is noscript fallback
	} {
		if !strings.Contains(html, want) {
			t.Errorf("live page missing %q", want)
		}
	}
}

func TestLivePageReportRenderedAsHTML(t *testing.T) {
	a := New(newServerTestDB(t), &MockGitHub{}, Config{})
	// Report with a GFM table, a details block, and a fenced diff block.
	report := "| A | B |\n|---|---|\n| 1 | 2 |\n\n<details><summary>Expand</summary>inner</details>"
	page := a.livePage(liveView{Repo: "r", Report: report})
	// Rendered HTML must contain real tags, not escaped entities.
	if !strings.Contains(page, "<table") {
		t.Error("report: expected rendered <table>, got escaped or missing")
	}
	if strings.Contains(page, "&lt;table") {
		t.Error("report: table tag must not be HTML-escaped")
	}
	if !strings.Contains(page, "<details") {
		t.Error("report: expected <details> passthrough, not escaped")
	}
	if !strings.Contains(page, "tfsp-report") {
		t.Error("report: expected .tfsp-report wrapper div")
	}
}

func TestLivePageHeaderLinks(t *testing.T) {
	a := New(newServerTestDB(t), &MockGitHub{}, Config{})
	page := a.livePage(liveView{
		Repo: "owner/repo", Environment: "nonprod", PR: 42,
		SHA:     "abc1234defgh",
		Context: "plan/nonprod",
	})
	// PR link
	if !strings.Contains(page, "github.com/owner/repo/pull/42") {
		t.Error("missing PR link")
	}
	if !strings.Contains(page, "#42") {
		t.Error("missing PR number display")
	}
	// Short SHA (first 7 chars)
	if !strings.Contains(page, "abc1234") {
		t.Error("missing short SHA")
	}
	// Checks link
	if !strings.Contains(page, "github.com/owner/repo/pull/42/checks") {
		t.Error("missing Checks link")
	}
	// Kind label in title
	if !strings.Contains(page, "Plan") {
		t.Error("missing Plan kind label in title")
	}
}

func TestLivePageHeaderLinksNoPR(t *testing.T) {
	a := New(newServerTestDB(t), &MockGitHub{}, Config{})
	page := a.livePage(liveView{
		Repo: "owner/repo", PR: 0,
		SHA:     "deadbeef12345",
		Context: "apply/nonprod",
	})
	// No PR link, but commit-based checks link
	if strings.Contains(page, "/pull/") {
		t.Error("no-PR page should not contain a /pull/ link")
	}
	if !strings.Contains(page, "github.com/owner/repo/commit/deadbeef12345") {
		t.Error("missing commit-based Checks link for no-PR execution")
	}
	// Apply kind
	if !strings.Contains(page, "Apply") {
		t.Error("missing Apply kind label in title")
	}
}

func TestRenderMarkdown(t *testing.T) {
	t.Run("empty input returns empty", func(t *testing.T) {
		if got := renderMarkdown(""); got != template.HTML("") {
			t.Errorf("renderMarkdown(\"\") = %q, want empty", got)
		}
	})

	t.Run("table renders as HTML", func(t *testing.T) {
		md := "| A | B |\n|---|---|\n| 1 | 2 |\n"
		got := string(renderMarkdown(md))
		if !strings.Contains(got, "<table") {
			t.Error("expected <table> in output")
		}
		if strings.Contains(got, "&lt;table") {
			t.Error("table tag must not be escaped")
		}
	})

	t.Run("details block passes through with unsafe", func(t *testing.T) {
		md := "<details><summary>Click</summary>inner content</details>"
		got := string(renderMarkdown(md))
		if !strings.Contains(got, "<details") {
			t.Errorf("expected <details> passthrough, got: %s", got)
		}
		if strings.Contains(got, "&lt;details") {
			t.Error("<details> must not be escaped")
		}
	})

	t.Run("fenced diff block renders as pre/code", func(t *testing.T) {
		md := "```diff\n-old\n+new\n```"
		got := string(renderMarkdown(md))
		if !strings.Contains(got, "<pre>") && !strings.Contains(got, "<code") {
			t.Errorf("expected pre/code for fenced block, got: %s", got)
		}
	})
}

func TestLivePageStackListAndTimeline(t *testing.T) {
	a := New(newServerTestDB(t), &MockGitHub{}, Config{})
	stacks := []events.StackState{
		{Path: "stacks/a", Project: "proj-a", Status: events.StatusGated},
		{Path: "stacks/b", Status: events.StatusSafe},
	}
	html := a.livePage(liveView{
		Exec: "e1", Repo: "o/r", Environment: "staging", Phase: events.PhasePlanning,
		Stacks: stacks, Report: "", SVG: `<svg id="dag"></svg>`, Panel: "",
	})
	for _, want := range []string{
		"stacks/a", "stacks/b",
		"badge-warning", "badge-success",
		"steps", "Plan",
		`<svg id="dag">`,
		"/live/e1/stack/stacks/a",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("live page missing %q", want)
		}
	}
}
