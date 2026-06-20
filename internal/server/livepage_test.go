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
		`href="/logs/e1/prod/api"`,  // detail "raw log ↗" → raw log endpoint
		`tfsp-report`,               // Result tab renders the plan diff
		`tabs tabs-lift`,            // DaisyUI tabs (label-wrapped radios)
		`<label class="tab"`,        // label-wrapped tab (hosts the live dot)
		`Log<span class="live-dot"`, // "connected live" dot on the Log tab (running exec)
		`class="shimmer`,            // Result shimmer for a not-yet-planned stack (stg/db)
		`class="term`,               // softened log surface
		`data-follow-url="/logs/e1/prod/api?follow=1"`, // running exec → live stream
		`/assets/term.js`,      // client-side ANSI+LineBuffer loaded from term.js
		`fonts.googleapis.com`, // Google Fonts link
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
	if len(m.Failures) != 0 { // no StatusFailed stacks → no triage cards
		t.Fatalf("no failed stacks → Failures must be empty, got %+v", m.Failures)
	}
}

func TestLivePageTriageSection(t *testing.T) {
	a := New(newServerTestDB(t), &MockGitHub{}, Config{})
	iamDetail := "Error: Error 403: Permission 'run.services.setIamPolicy' denied on resource 'projects/my-project/locations/us-central1/services/api' to user 'sa@my-project.iam.gserviceaccount.com'"
	page := a.livePage(liveView{
		Exec:        "exec-abc",
		Repo:        "o/r",
		Environment: "nonprod",
		Context:     "apply/nonprod",
		Stacks: []events.StackState{
			{Path: "stacks/api", Status: events.StatusFailed, Detail: iamDetail},
			{Path: "stacks/db", Status: events.StatusSafe},
		},
		StackReports: map[string]string{},
		StackLogs:    map[string]string{},
	})
	for _, want := range []string{
		"Needs attention",            // section heading
		"stacks/api",                 // failed stack path
		"Likely cause",               // cause label
		"PAM grant",                  // IAM-specific cause text (matches the classifier)
		"Re-request elevated access", // first next step from IAM matcher
		"Safe to retry",              // state-impact text from IAM matcher
		"#stack-stacks-api",          // anchor link to the stack's detail pane
		"/logs/exec-abc/stacks/api",  // raw log URL
	} {
		if !strings.Contains(page, want) {
			t.Errorf("triage section missing %q", want)
		}
	}
	// Only the failed stack becomes a triage card — the safe stack must not.
	// (The safe stack still shows in the stacks list below, which is why this is
	// asserted at the model level rather than by searching the rendered page.)
	m := buildLiveModel(liveView{
		Exec:    "exec-abc",
		Context: "apply/nonprod",
		Stacks: []events.StackState{
			{Path: "stacks/api", Status: events.StatusFailed, Detail: iamDetail},
			{Path: "stacks/db", Status: events.StatusSafe},
		},
	}, "apply", true, time.Now())
	if len(m.Failures) != 1 || m.Failures[0].Path != "stacks/api" {
		t.Fatalf("expected exactly the failed stack as a triage card, got %+v", m.Failures)
	}

	// A failure with no captured detail gets no card (mirrors failuresSection):
	// nothing to triage, so "Needs attention" stays meaningful.
	m2 := buildLiveModel(liveView{
		Exec:    "exec-abc",
		Context: "apply/nonprod",
		Stacks:  []events.StackState{{Path: "stacks/api", Status: events.StatusFailed}},
	}, "apply", true, time.Now())
	if len(m2.Failures) != 0 {
		t.Fatalf("failed stack with empty Detail must produce no triage card, got %+v", m2.Failures)
	}
}

func TestBuildLiveModelPlanFinished(t *testing.T) {
	m := buildLiveModel(liveView{Context: "plan", Status: ""}, "plan", true, time.Now())
	if m.PhaseAccent != "plan" || m.PhaseLabel != "PLANNED" {
		t.Fatalf("plan finished: accent=%q label=%q", m.PhaseAccent, m.PhaseLabel)
	}
}

// TestBuildLiveModelWarmingInitializingLabels asserts that warming and initializing
// phases surface correctly on the live page for both plan and apply kinds (Task 4).
// Finished executions always use their terminal label even if the stored phase is stale.
func TestBuildLiveModelWarmingInitializingLabels(t *testing.T) {
	cases := []struct {
		name     string
		view     liveView
		kind     string
		finished bool
		want     string
	}{
		{
			name:     "plan warming",
			view:     liveView{Context: "", Phase: events.PhaseWarming},
			kind:     "plan",
			finished: false,
			want:     "WARMING",
		},
		{
			name:     "apply initializing",
			view:     liveView{Context: "apply/prod", Phase: events.PhaseInitializing},
			kind:     "apply",
			finished: false,
			want:     "INITIALIZING",
		},
		{
			name:     "plan initializing",
			view:     liveView{Context: "", Phase: events.PhaseInitializing},
			kind:     "plan",
			finished: false,
			want:     "INITIALIZING",
		},
		{
			name:     "finished apply with stale warming phase",
			view:     liveView{Context: "apply/prod", Phase: events.PhaseWarming, Status: "success"},
			kind:     "apply",
			finished: true,
			want:     "APPLIED",
		},
		{
			name:     "finished plan with stale warming phase",
			view:     liveView{Context: "", Phase: events.PhaseWarming, Report: "# report"},
			kind:     "plan",
			finished: true,
			want:     "PLANNED",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := buildLiveModel(tc.view, tc.kind, tc.finished, time.Now())
			if m.PhaseLabel != tc.want {
				t.Errorf("PhaseLabel = %q, want %q", m.PhaseLabel, tc.want)
			}
		})
	}
}

// TestMovedStackRow asserts that a StatusMoving stack with no report yields
// Moved==true and HasDetail/Detail empty.
func TestMovedStackRow(t *testing.T) {
	m := buildLiveModel(liveView{
		Context: "apply/nonprod",
		Stacks: []events.StackState{
			{Path: "stacks/mv", Status: events.StatusMoving},
		},
		StackReports: map[string]string{},
		StackLogs:    map[string]string{},
	}, "apply", false, time.Now())
	if len(m.Groups) == 0 || len(m.Groups[0].Stacks) == 0 {
		t.Fatal("expected at least one stack row")
	}
	row := m.Groups[0].Stacks[0]
	if !row.Moved {
		t.Errorf("StatusMoving stack: Moved=%v, want true", row.Moved)
	}
	if row.HasDetail {
		t.Errorf("StatusMoving stack with no report: HasDetail=%v, want false", row.HasDetail)
	}
	if row.Detail != "" {
		t.Errorf("StatusMoving stack with no report: Detail=%q, want empty", row.Detail)
	}
}

// TestLivePageTabDefaultByLiveness asserts that for a running (Follow) stack the
// Log radio is checked and Result is not; for a finished stack the Result radio is
// checked. Also asserts that a StatusMoving stack shows the "State-only move" text.
func TestLivePageTabDefaultByLiveness(t *testing.T) {
	a := New(newServerTestDB(t), &MockGitHub{}, Config{})

	// Running execution: Follow=true → Log radio should be checked, Result not.
	runningPage := a.livePage(liveView{
		Exec:    "e-run",
		Repo:    "o/r",
		Context: "apply/nonprod",
		Status:  "in_progress",
		Stacks: []events.StackState{
			{Path: "stacks/a", Status: events.StatusRunning},
		},
		StackReports: map[string]string{},
		StackLogs:    map[string]string{},
	})
	// The Log radio must have "checked"; the Result radio must not.
	// Running exec renders: Result=`name="..." >Result` (unchecked), Log=`name="..." checked>`
	if !strings.Contains(runningPage, `name="tab-stack-stacks-a" checked`) {
		t.Error("running exec: Log radio should be checked (Follow=true)")
	}
	// The Result radio must NOT be checked: no "checked>Result" in the page.
	if strings.Contains(runningPage, "checked>Result") {
		t.Error("running exec: Result radio must not be checked when Follow=true")
	}

	// Finished execution: Follow=false → Result radio should be checked, Log not.
	finishedPage := a.livePage(liveView{
		Exec:    "e-done",
		Repo:    "o/r",
		Context: "plan/nonprod",
		Status:  "",
		Report:  "# summary", // non-empty report → finished plan
		Stacks: []events.StackState{
			{Path: "stacks/b", Status: events.StatusSafe},
		},
		StackReports: map[string]string{"stacks/b": "## stacks/b\nno changes"},
		StackLogs:    map[string]string{},
	})
	// Result radio comes first and must be checked; Log radio follows and must not be.
	if !strings.Contains(finishedPage, "checked>Result") {
		t.Error("finished exec: Result radio should be checked (Follow=false)")
	}
	// Log radio in the finished case renders as: name="tab-..." >Log (no "checked" before "Log")
	if strings.Contains(finishedPage, `checked>Log`) {
		t.Error("finished exec: Log radio must not be checked when Follow=false")
	}

	// Move-only stack: Moved=true, finished → shows "State-only move" text.
	movePage := a.livePage(liveView{
		Exec:    "e-mv",
		Repo:    "o/r",
		Context: "apply/nonprod",
		Status:  "success",
		Stacks: []events.StackState{
			{Path: "stacks/mv", Status: events.StatusMoving},
		},
		StackReports: map[string]string{},
		StackLogs:    map[string]string{},
	})
	if !strings.Contains(movePage, "State-only move") {
		t.Error("StatusMoving stack: expected 'State-only move' placeholder text")
	}
}

// TestLivePageInProgressMoveShowsShimmer asserts that an in-progress execution
// with a StatusMoving stack renders the shimmer (Follow=true winning over Moved=true
// in the template's else-if chain), NOT the "State-only move" text.
func TestLivePageInProgressMoveShowsShimmer(t *testing.T) {
	a := New(newServerTestDB(t), &MockGitHub{}, Config{})

	// In-progress move: Status="" (not finished) + StatusMoving stack.
	// Template order: Detail → else if Follow → else if Moved → else.
	// Follow=true (not finished) should win and show shimmer, not "State-only move".
	page := a.livePage(liveView{
		Exec:    "e-moving",
		Repo:    "o/r",
		Context: "apply/prod",
		Status:  "", // empty status = not finished → Follow=true
		Stacks: []events.StackState{
			{Path: "stacks/mv", Status: events.StatusMoving},
		},
		StackReports: map[string]string{},
		StackLogs:    map[string]string{},
	})

	// Must contain shimmer (the Follow branch rendering).
	if !strings.Contains(page, `class="shimmer`) {
		t.Error("in-progress move: expected shimmer placeholder (Follow=true), not found")
	}

	// Must NOT contain "State-only move" text (the Moved branch is skipped by Follow winning).
	if strings.Contains(page, "State-only move") {
		t.Error("in-progress move: must not show 'State-only move' text (Follow wins over Moved in template)")
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
