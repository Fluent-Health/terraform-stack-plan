package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/config"
	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// A plan check-run update carries the rich summary (blast-radius headline +
// per-stack table) and the correct "Terraform plan" title.
func TestPlanCheckRunSummaryAndTitle(t *testing.T) {
	db := newServerTestDB(t)
	var title, summary string
	gh := &MockGitHub{
		CreateCheckRunFn: func(_ context.Context, _, _, _ string, _ string) (int64, error) { return 11, nil },
		UpdateCheckRunFn: func(_ context.Context, _ string, _ int64, u CheckRunUpdate) error {
			title, summary = u.Title, u.Summary
			return nil
		},
	}
	a := New(db, gh, Config{PublicBaseURL: "https://serve.example"})

	initBody, _ := json.Marshal(events.Init{
		ID: "plan-1", Repo: "o/r", SHA: "abc", PR: 3, Environment: "nonprod",
		Stacks: []events.StackState{{Path: "svc/a", Status: events.StatusPending}},
	})
	rec := httptest.NewRecorder()
	a.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/init", bytes.NewReader(initBody)))
	if rec.Code != http.StatusOK {
		t.Fatalf("init: %d %s", rec.Code, rec.Body.String())
	}

	updBody, _ := json.Marshal(events.Update{ID: "plan-1", Stack: "svc/a", Status: events.StatusPlanned})
	rec = httptest.NewRecorder()
	a.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/update", bytes.NewReader(updBody)))
	if rec.Code != http.StatusOK {
		t.Fatalf("update: %d", rec.Code)
	}

	for _, want := range []string{"⠿", "planned"} {
		if !strings.Contains(title, want) {
			t.Errorf("title missing %q in: %q", want, title)
		}
	}
	// No headline — check-run title carries the status line.
	if strings.HasPrefix(summary, "## ") {
		t.Errorf("summary must not start with a ## headline; got:\n%s", summary)
	}
	for _, want := range []string{"| Stack | Ops | Risk | State |", "`svc/a`"} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary missing %q in:\n%s", want, summary)
		}
	}
}

func TestProgressTitleTerminalShowsCounts(t *testing.T) {
	stacks := []events.StackState{
		{Path: "a", Status: events.StatusPlanned, Counts: &events.Counts{Add: 6}},
		{Path: "b", Status: events.StatusPlanned, Counts: &events.Counts{Change: 3, Destroy: 2}},
	}
	got := progressTitle(nil, events.PhasePlanning, stacks, true, "plan")
	for _, want := range []string{"+6", "~3", "−2", "2 stacks"} {
		if !strings.Contains(got, want) {
			t.Errorf("terminal title %q missing %q", got, want)
		}
	}
	if strings.Contains(got, "⠿") || strings.Contains(got, "⠀") {
		t.Errorf("terminal title must not contain the progress bar: %q", got)
	}
}

func TestProgressTitleTerminalNoChanges(t *testing.T) {
	if got := progressTitle(nil, events.PhasePlanning, nil, true, "plan"); !strings.Contains(got, "no changes") {
		t.Errorf("empty terminal title = %q, want it to say 'no changes'", got)
	}
}

func TestProgressTitleRunningStillHasBar(t *testing.T) {
	stacks := []events.StackState{{Path: "a", Status: events.StatusRunning}}
	if got := progressTitle(nil, events.PhasePlanning, stacks, false, "plan"); !strings.Contains(got, "⠿") {
		t.Errorf("running title %q must keep the bar", got)
	}
}

// The bar is overall progress; the label already carries the per-phase count
// (e.g. "applying 1/4"). The title must NOT repeat that count between the bar and
// the label — that double indicator ("█… 1/4 · applying 1/4") was the bug.
func TestProgressTitleNoDoubleCount(t *testing.T) {
	stacks := make([]events.StackState, 4)
	for i := range stacks {
		stacks[i] = events.StackState{Path: fmt.Sprintf("svc/%d", i), Status: events.StatusPending}
	}
	stacks[0].Status = events.StatusSafe // 1 of 4 applied
	got := progressTitle(nil, events.PhaseApplying, stacks, false, "apply")
	if strings.Count(got, "1/4") != 1 {
		t.Errorf("title %q should mention the count exactly once (no double indicator)", got)
	}
	if !strings.Contains(got, "applying 1/4") {
		t.Errorf("title %q should carry the count in the label", got)
	}
}

func TestProgressTitlePreparing(t *testing.T) {
	// apply, non-terminal, PhaseInitializing, 3 of 8 stacks re-planned → "preparing"
	stacks := make([]events.StackState, 8)
	for i := range stacks {
		stacks[i] = events.StackState{Path: fmt.Sprintf("svc/%d", i), Status: events.StatusPending}
	}
	// Mark 3 as StatusPlanned (done re-planning)
	stacks[0].Status = events.StatusPlanned
	stacks[1].Status = events.StatusPlanned
	stacks[2].Status = events.StatusPlanned

	got := progressTitle(nil, events.PhaseInitializing, stacks, false, "apply")
	if !strings.Contains(got, "3/8") {
		t.Errorf("preparing title %q missing 3/8", got)
	}
	if !strings.Contains(got, "preparing") {
		t.Errorf("preparing title %q missing 'preparing'", got)
	}
	if strings.Contains(got, "initializing") {
		t.Errorf("preparing title %q must not say 'initializing'", got)
	}

	// apply, non-terminal, PhasePlanning, 0 done → "preparing"
	all8pending := make([]events.StackState, 8)
	for i := range all8pending {
		all8pending[i] = events.StackState{Path: fmt.Sprintf("svc/%d", i), Status: events.StatusPending}
	}
	got2 := progressTitle(nil, events.PhasePlanning, all8pending, false, "apply")
	if !strings.Contains(got2, "preparing") {
		t.Errorf("planning-phase apply title %q missing 'preparing'", got2)
	}
	if !strings.Contains(got2, "0/8") {
		t.Errorf("planning-phase apply title %q missing '0/8'", got2)
	}

	// apply, non-terminal, PhaseApplying → "applying" (prep branch must NOT fire)
	got3 := progressTitle(nil, events.PhaseApplying, stacks, false, "apply")
	if !strings.Contains(got3, "applying") {
		t.Errorf("applying-phase title %q missing 'applying'", got3)
	}
	if strings.Contains(got3, "preparing") {
		t.Errorf("applying-phase title %q must not say 'preparing'", got3)
	}

	// plan, non-terminal, PhaseInitializing → still "initializing" (unchanged)
	got4 := progressTitle(nil, events.PhaseInitializing, all8pending, false, "plan")
	if !strings.Contains(got4, "initializing") {
		t.Errorf("plan initializing title %q missing 'initializing'", got4)
	}
	if strings.Contains(got4, "preparing") {
		t.Errorf("plan initializing title %q must not say 'preparing'", got4)
	}
}

// The apply's pre-apply ("preparing") stage must fill the SAME unified weighted
// bar as the rest of the apply — the warming + initializing bands — instead of a
// separate prepared/total scale that sits at 0% and then jumps to the applying
// band. (Apply weights: warming 1 + initializing 2 + applying 10 + verifying 1 = 14.)
func TestRunProgressPreparingUsesUnifiedWeightedBar(t *testing.T) {
	prog := &config.ProgressConfig{Apply: []config.PhaseWeight{
		{Phase: events.PhaseWarming, Weight: 1},
		{Phase: events.PhaseInitializing, Weight: 2},
		{Phase: events.PhaseApplying, Weight: 10},
		{Phase: events.PhaseVerifying, Weight: 1},
	}}
	three := func(s events.Status) []events.StackState {
		return []events.StackState{{Path: "a", Status: s}, {Path: "b", Status: s}, {Path: "c", Status: s}}
	}

	// Warming entered (marker band) → 1/14 ≈ 7%, not 0%.
	if _, pct, label := runProgress(prog, events.PhaseWarming, three(events.StatusPending), "apply"); pct != 7 {
		t.Fatalf("warming pct = %d, want 7 (warming band); label=%q", pct, label)
	}

	// Initializing, all three initialized → (1 + 2)/14 ≈ 21%, and the label stays
	// "preparing" (not "initializing"/"applying").
	_, pct, label := runProgress(prog, events.PhaseInitializing, three(events.StatusInitialized), "apply")
	if pct != 21 {
		t.Fatalf("init-complete pct = %d, want 21 (warming+init band)", pct)
	}
	if !strings.Contains(label, "preparing") {
		t.Fatalf("preparing label = %q, want 'preparing'", label)
	}
}

func TestBackfillPrefersLogErrorTail(t *testing.T) {
	db := newServerTestDB(t)
	a := New(db, &MockGitHub{}, Config{})

	// Store the real terraform error as the stack's log excerpt.
	const logExcerpt = "module.x: Creating...\n╷\n│ Error: googleapi: Error 403: permission denied\n╵\n"
	if err := store.UpsertStackOutput(db, "exec-detail", "a", "log", "", logExcerpt); err != nil {
		t.Fatal(err)
	}
	// The stack has a generic tick detail ("terraform apply failed") — the real
	// error is in the captured log. After backfill the real error must win.
	g := events.Graph{Stacks: []events.StackState{
		{Path: "a", Status: events.StatusFailed, Detail: "terraform apply failed"},
	}}
	a.backfillFailureDetail("exec-detail", &g)
	if !strings.Contains(g.Stacks[0].Detail, "Error 403") {
		t.Errorf("Detail = %q, want the real 403 from the log, not the generic tick detail", g.Stacks[0].Detail)
	}
}

func TestBackfillFailureDetailFromLog(t *testing.T) {
	db := newServerTestDB(t)
	a := New(db, &MockGitHub{}, Config{})

	// A failed stack with NO detail but a stored log excerpt holding an error block.
	if err := store.UpsertStackOutput(db, "e1", "svc/x", "log", "",
		"applying...\n╷\n│ Error: googleapi 403 setIamPolicy\n╵\n"); err != nil {
		t.Fatal(err)
	}
	g := events.Graph{Stacks: []events.StackState{
		{Path: "svc/x", Status: events.StatusFailed},                 // no Detail → backfilled
		{Path: "svc/y", Status: events.StatusFailed, Detail: "kept"}, // has Detail → untouched
		{Path: "svc/z", Status: events.StatusFailed},                 // no log → stays empty
	}}
	a.backfillFailureDetail("e1", &g)

	if !strings.Contains(g.Stacks[0].Detail, "Error: googleapi 403 setIamPolicy") {
		t.Errorf("svc/x detail not backfilled: %q", g.Stacks[0].Detail)
	}
	if g.Stacks[1].Detail != "kept" {
		t.Errorf("svc/y detail clobbered: %q", g.Stacks[1].Detail)
	}
	if g.Stacks[2].Detail != "" {
		t.Errorf("svc/z should stay empty: %q", g.Stacks[2].Detail)
	}
}

func TestTerminalSummaryApplyTallies(t *testing.T) {
	stacks := []events.StackState{
		{Path: "a", Status: events.StatusSafe, Counts: &events.Counts{Change: 20}},
		{Path: "b", Status: events.StatusNochange},
		{Path: "c", Status: events.StatusFailed},
		{Path: "d", Status: events.StatusAborted},
	}
	got := terminalSummary("Apply", stacks)
	for _, want := range []string{"applied 2/4", "1 no-change", "1 failed", "1 aborted"} {
		if !strings.Contains(got, want) {
			t.Errorf("terminalSummary = %q, missing %q", got, want)
		}
	}
}
