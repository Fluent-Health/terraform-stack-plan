package server

import (
	"html/template"
	"strings"
	"testing"
	"time"

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
	for _, want := range []string{"proj-a", "proj-b", "proj-c", "iam", "database", "Approved", "Awaiting approval", "Blocked"} {
		if !strings.Contains(panel, want) {
			t.Errorf("panel missing %q", want)
		}
	}
	// Pending gates get an "approve in PAM" deep-link to the target's grants page;
	// the approved one does not.
	if !strings.Contains(panel, "approve in PAM") || !strings.Contains(panel, pamConsoleURL("proj-b")) {
		t.Error("pending gate must link to the PAM console to approve")
	}
	if strings.Contains(panel, pamConsoleURL("proj-a")) {
		t.Error("approved gate must not show an approve link")
	}
}

func TestLivePageRendersShell(t *testing.T) {
	a := New(newServerTestDB(t), &MockGitHub{}, Config{})
	html := a.livePage(liveView{
		Repo: "octo/repo", Environment: "staging",
		Stacks:       []events.StackState{{Path: "s1"}},
		StackReports: map[string]string{"s1": "PLAN_REPORT_BODY"},
		SVG:          `<svg id="dag"></svg>`, Panel: `<div class="panel">P</div>`,
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
	page := a.livePage(liveView{Repo: "r", Stacks: []events.StackState{{Path: "s1"}}, StackReports: map[string]string{"s1": report}})
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
		"sl-blocked", // gated stack → blocked label colour
		"sl-planned", // safe stack in a plan execution
		"proj-a",     // project group name
		"Plan",       // kind label in the title
		"menu menu-sm",
		`<svg id="dag">`, // DAG injected
		"collapse-arrow", // DaisyUI collapse for the demoted DAG
		"/logs/e1/stacks/a",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("live page missing %q", want)
		}
	}
}

func TestLivePageBriefingBand(t *testing.T) {
	a := New(newServerTestDB(t), &MockGitHub{}, Config{})
	html := a.livePage(liveView{
		Exec: "e1", Repo: "o/r", Environment: "nonprod", Context: "apply/nonprod",
		Status: "in_progress", CreatedAt: time.Now().Add(-90 * time.Second),
		Stacks: []events.StackState{
			{Path: "prod/api", Project: "fh-prod-host", Status: events.StatusRunning,
				Counts: &events.Counts{Change: 8}, Categories: []events.Category{{Name: "iam"}}},
			{Path: "stg/db", Project: "fh-staging-host", Status: events.StatusSafe,
				Counts: &events.Counts{Destroy: 2}, Categories: []events.Category{{Name: "destructive"}}},
		},
		StackReports: map[string]string{"prod/api": "## prod/api\n~ google_cloud_run_service.api"},
	})
	for _, want := range []string{
		`badge-primary`,        // apply-phase pill (DaisyUI badge, primary slot)
		`APPLYING`,             // phase label
		`class="progress-bar"`, // live progress bar (the one bespoke piece)
		`bar-seg bs-applying`,  // segment coloured by current state (prod/api)
		`bar-seg bs-applied`,   // applied segment (stg/db)
		`sl-applying`,          // running apply → applying label colour
		`sl-applied`,           // safe apply → applied
		`text-warning">⚿1`,     // IAM count cell (warning slot)
		`badge-error`,          // destructive flag → error badge
		`⚠ Destructive`,
		`⚿ IAM`, `⚠ destructive`, // per-stack risk badges
		`menu menu-sm`,
		`href="/logs/e1/prod/api"`, // detail "raw log ↗" → raw log endpoint
		`tfsp-report`,              // Result tab renders the plan diff
		`tabs tabs-lift`,           // DaisyUI tabs in the detail pane
		`aria-label="Result"`, `aria-label="Log"`,
		`class="term`, // softened log surface
		`data-follow-url="/logs/e1/prod/api?follow=1"`, // running exec → live stream
		`fonts.googleapis.com`,                         // Google Fonts link
	} {
		if !strings.Contains(html, want) {
			t.Errorf("briefing live page missing %q", want)
		}
	}
}

func TestBuildLiveModel(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 5, 12, 0, time.UTC)
	created := now.Add(-(4*time.Minute + 12*time.Second))
	v := liveView{
		Exec: "e1", Repo: "Fluent-Health/infra", PR: 412, SHA: "a3f1414abc",
		Environment: "nonprod", Context: "apply/nonprod", Status: "in_progress",
		CreatedAt: created,
		Stacks: []events.StackState{
			{Path: "prod/api", Project: "fh-prod-host", Status: events.StatusRunning,
				Counts: &events.Counts{Change: 8}, Categories: []events.Category{{Name: "iam"}}},
			{Path: "stg/db", Project: "fh-staging-host", Status: events.StatusSafe,
				Counts: &events.Counts{Destroy: 2}, Categories: []events.Category{{Name: "destructive"}}},
		},
	}
	m := buildLiveModel(v, "apply", false, now)

	if m.PhaseAccent != "apply" {
		t.Fatalf("PhaseAccent=%q", m.PhaseAccent)
	}
	if m.PhaseLabel != "APPLYING" {
		t.Fatalf("PhaseLabel=%q", m.PhaseLabel)
	}
	if m.Elapsed != "4m 12s" {
		t.Fatalf("Elapsed=%q", m.Elapsed)
	}
	if m.Verdict.Change != 8 || m.Verdict.Destroy != 2 || m.Verdict.TotalOps != 10 {
		t.Fatalf("verdict=%+v", m.Verdict)
	}
	if !m.Destructive || m.IAMCount != 1 {
		t.Fatalf("flags: destructive=%v iamCount=%d", m.Destructive, m.IAMCount)
	}
	if len(m.Progress) != 2 {
		t.Fatalf("progress segs=%d", len(m.Progress))
	}
	if m.Progress[0].StateCSS != "applying" { // prod/api is Running in an apply
		t.Fatalf("progress[0]=%+v", m.Progress[0])
	}
	if len(m.Groups) != 2 || m.Groups[0].Name != "fh-prod-host" {
		t.Fatalf("groups=%+v", m.Groups)
	}
	r0 := m.Groups[0].Stacks[0]
	if r0.Path != "prod/api" || r0.State.CSS != "applying" || r0.Ops != "~8" || len(r0.Risks) != 1 {
		t.Fatalf("row0=%+v", r0)
	}
	if m.ShortSHA != "a3f1414" {
		t.Fatalf("ShortSHA=%q", m.ShortSHA)
	}
}

func TestBuildLiveModelPlanFinished(t *testing.T) {
	m := buildLiveModel(liveView{Context: "plan", Status: ""}, "plan", true, time.Now())
	if m.PhaseAccent != "plan" || m.PhaseLabel != "PLANNED" {
		t.Fatalf("plan finished: accent=%q label=%q", m.PhaseAccent, m.PhaseLabel)
	}
}

func TestHumanizeDuration(t *testing.T) {
	if got := humanizeDuration(4*time.Minute + 12*time.Second); got != "4m 12s" {
		t.Fatalf("got %q", got)
	}
	if got := humanizeDuration(45 * time.Second); got != "45s" {
		t.Fatalf("got %q", got)
	}
	if got := humanizeDuration(time.Hour + 5*time.Minute); got != "1h 5m" {
		t.Fatalf("got %q", got)
	}
}
