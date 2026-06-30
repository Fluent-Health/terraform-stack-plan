package server

import (
	"html/template"
	"net/http"
	"net/http/httptest"
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
		Stacks: []events.StackState{{Path: "s1"}},
		SVG:    `<svg id="dag"></svg>`, Panel: `<div class="panel">P</div>`,
	})
	for _, want := range []string{
		`/assets/app.css`,                      // links the embedded stylesheet
		`data-theme`,                           // DaisyUI theme on <html>
		`octo/repo`,                            // repo shown
		`staging`,                              // environment shown
		`<svg id="dag">`,                       // trusted SVG injected un-escaped
		`class="panel"`,                        // trusted panel injected un-escaped
		`data-plan-url=`,                       // Result pane carries lazy-fetch URL
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
	// Per-stack plan diffs are now lazy-fetched via /plan/{exec}/{stack}.
	// The live page should carry tfsp-report containers with data-plan-url.
	page := a.livePage(liveView{
		Exec: "exec1", Repo: "r",
		Stacks: []events.StackState{{Path: "svc/a"}},
	})
	// The Result pane emits a lazy-fetch container (not inline diff HTML).
	if !strings.Contains(page, "tfsp-report") {
		t.Error("report: expected .tfsp-report lazy container div")
	}
	if !strings.Contains(page, `data-plan-url="/plan/exec1/svc/a"`) {
		t.Error("report: expected data-plan-url on the lazy container")
	}
	// renderMarkdown is covered by TestRenderMarkdown; goldmark output verified there.
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
		Status: "in_progress", Phase: events.PhaseApplying, CreatedAt: time.Now().Add(-90 * time.Second),
		Stacks: []events.StackState{
			{Path: "prod/api", Project: "fh-prod-host", Status: events.StatusRunning,
				Counts: &events.Counts{Change: 8}, Categories: []events.Category{{Name: "iam"}}},
			{Path: "stg/db", Project: "fh-staging-host", Status: events.StatusSafe,
				Counts: &events.Counts{Destroy: 2}, Categories: []events.Category{{Name: "destructive"}}},
		},
	})
	for _, want := range []string{
		`badge-primary`,        // apply-phase pill (DaisyUI badge, primary slot)
		`APPLYING`,             // phase label
		`class="progress-bar"`, // overall weighted progress bar
		`bar-seg bs-applying`,  // filled portion of the overall bar
		`bar-seg bs-queued`,    // unfilled remainder
		`50%`,                  // 1 of 2 stacks applied → overall 50%
		`applying 1/2`,         // per-phase label beside the bar
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
		Phase: events.PhaseApplying, CreatedAt: created,
		Stacks: []events.StackState{
			{Path: "prod/api", Project: "fh-prod-host", Status: events.StatusRunning,
				Counts: &events.Counts{Change: 8}, Categories: []events.Category{{Name: "iam"}}},
			{Path: "stg/db", Project: "fh-staging-host", Status: events.StatusSafe,
				Counts: &events.Counts{Destroy: 2}, Categories: []events.Category{{Name: "destructive"}}},
		},
	}
	m := buildLiveModel(v, "apply", false, nil, now)

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
	// One overall bar: 1 of 2 stacks done (stg/db Safe) under PhaseApplying → 50%.
	if m.ProgressPct != 50 || m.ProgressRemain != 50 {
		t.Fatalf("progress pct=%d remain=%d, want 50/50", m.ProgressPct, m.ProgressRemain)
	}
	if m.ProgressLabel != "applying 1/2" {
		t.Fatalf("progress label=%q, want %q", m.ProgressLabel, "applying 1/2")
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
		StackLogs: map[string]string{},
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
	}, "apply", true, nil, time.Now())
	if len(m.Failures) != 1 || m.Failures[0].Path != "stacks/api" {
		t.Fatalf("expected exactly the failed stack as a triage card, got %+v", m.Failures)
	}

	// A failure with no captured detail gets no card (mirrors failuresSection):
	// nothing to triage, so "Needs attention" stays meaningful.
	m2 := buildLiveModel(liveView{
		Exec:    "exec-abc",
		Context: "apply/nonprod",
		Stacks:  []events.StackState{{Path: "stacks/api", Status: events.StatusFailed}},
	}, "apply", true, nil, time.Now())
	if len(m2.Failures) != 0 {
		t.Fatalf("failed stack with empty Detail must produce no triage card, got %+v", m2.Failures)
	}

	// Synthesized execution-level triage card on overall run failure with report but no failed stacks:
	m3 := buildLiveModel(liveView{
		Exec:    "exec-abc",
		Context: "apply/nonprod",
		Status:  "failure",
		Report:  "cross-state move failed: uncommitted changes left in the repository",
		Stacks: []events.StackState{
			{Path: "stacks/api", Status: events.StatusAborted},
		},
	}, "apply", true, nil, time.Now())
	if len(m3.Failures) != 1 || m3.Failures[0].Path != "Execution Failure" {
		t.Fatalf("expected synthesized execution failure triage card, got %+v", m3.Failures)
	}
	if !strings.Contains(m3.Failures[0].Cause, "Cross-state move") && !strings.Contains(m3.Failures[0].StateImpact, "Cross-state move") {
		t.Errorf("expected cross-state move cause/impact, got: %+v", m3.Failures[0])
	}
}

func TestBuildLiveModelPlanFinished(t *testing.T) {
	m := buildLiveModel(liveView{Context: "plan", Status: ""}, "plan", true, nil, time.Now())
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
			want:     "PREPARING",
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
			m := buildLiveModel(tc.view, tc.kind, tc.finished, nil, time.Now())
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
		StackLogs: map[string]string{},
	}, "apply", false, nil, time.Now())
	if len(m.Groups) == 0 || len(m.Groups[0].Stacks) == 0 {
		t.Fatal("expected at least one stack row")
	}
	row := m.Groups[0].Stacks[0]
	if !row.Moved {
		t.Errorf("StatusMoving stack: Moved=%v, want true", row.Moved)
	}
	// PlanURL is always set (lazy fetch); Moved flag governs what the template shows.
	if row.PlanURL == "" {
		t.Errorf("StatusMoving stack: PlanURL must be set even for moves")
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
		StackLogs: map[string]string{},
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
		StackLogs: map[string]string{},
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
		StackLogs: map[string]string{},
	})
	if !strings.Contains(movePage, "State-only move") {
		t.Error("StatusMoving stack: expected 'State-only move' placeholder text")
	}
}

// TestLivePageMovedResultPane asserts that a StatusMoving stack always shows the
// "State-only move" text in the Result pane regardless of liveness, and that a
// non-moved stack always gets the lazy-fetch tfsp-report container with shimmer.
func TestLivePageMovedResultPane(t *testing.T) {
	a := New(newServerTestDB(t), &MockGitHub{}, Config{})

	// In-progress move: StatusMoving + not finished.
	// New template order: if Moved → "State-only move"; else → tfsp-report+shimmer.
	// Moved=true wins unconditionally in the Result pane.
	inProgressPage := a.livePage(liveView{
		Exec:    "e-moving",
		Repo:    "o/r",
		Context: "apply/prod",
		Status:  "", // empty status = not finished → Follow=true
		Stacks: []events.StackState{
			{Path: "stacks/mv", Status: events.StatusMoving},
		},
		StackLogs: map[string]string{},
	})

	// Moved stack always shows "State-only move", never the shimmer in the Result pane.
	if !strings.Contains(inProgressPage, "State-only move") {
		t.Error("in-progress move: must show 'State-only move' text (Moved wins in new template)")
	}

	// A non-moved stack in a running exec gets the lazy-fetch container with shimmer.
	runningPage := a.livePage(liveView{
		Exec:    "e-running",
		Repo:    "o/r",
		Context: "apply/prod",
		Status:  "",
		Stacks: []events.StackState{
			{Path: "stacks/api", Status: events.StatusRunning},
		},
		StackLogs: map[string]string{},
	})
	if !strings.Contains(runningPage, `class="shimmer`) {
		t.Error("non-moved running stack: expected shimmer inside tfsp-report container")
	}
	if !strings.Contains(runningPage, `data-plan-url=`) {
		t.Error("non-moved running stack: expected data-plan-url on the lazy container")
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

func TestLivePageDoesNotEmbedPlanMarkdown(t *testing.T) {
	a := New(newServerTestDB(t), &MockGitHub{}, Config{})
	id := "exec-lazy"
	if err := store.UpsertInit(a.db, events.Init{ID: id, Repo: "o/r", Context: "plan/nonprod", Environment: "nonprod",
		Stacks: []events.StackState{{Path: "svc/a", Status: events.StatusPlanned}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertStackOutput(a.db, id, "svc/a", "plan", "", "UNIQUEPLANMARKER diff body"); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	a.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/live/"+id, nil))
	body := rec.Body.String()
	if strings.Contains(body, "UNIQUEPLANMARKER") {
		t.Errorf("live page still embeds plan markdown; should be lazy-fetched")
	}
	if !strings.Contains(body, `data-plan-url="/plan/`+id+`/svc/a"`) {
		t.Errorf("Result pane missing data-plan-url")
	}
}
