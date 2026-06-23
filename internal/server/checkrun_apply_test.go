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

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// A post-merge apply execution surfaces BOTH ways: the apply/<env> commit
// status (colors the commit-list icon) and a check run named apply/<env>
// (visible on the Checks page next to the CI run). This test reversed the
// old no-check-run rule: check runs on default-branch commits display fine.
func TestApplyInitCreatesApplyCheckRun(t *testing.T) {
	db := newServerTestDB(t)
	var gotName string
	gh := &MockGitHub{
		CreateCheckRunFn: func(_ context.Context, _, _, name, _ string) (int64, error) {
			gotName = name
			return 42, nil
		},
	}
	a := New(db, gh, Config{PublicBaseURL: "https://serve.example"})

	body, _ := json.Marshal(events.Init{
		ID: "apply-1", Repo: "o/r", SHA: "deadbeef", PR: 7, Environment: "nonprod",
		Context: "apply/nonprod",
		Stacks:  []events.StackState{{Path: "a", Status: events.StatusPending}},
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/init", bytes.NewReader(body))
	a.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("init: %d %s", rec.Code, rec.Body.String())
	}
	if gotName != "apply/nonprod" {
		t.Fatalf("check run name = %q, want apply/nonprod", gotName)
	}
}

func TestApplyDriveUpdatesCheckRun(t *testing.T) {
	db := newServerTestDB(t)
	var updatedID int64
	var updatedSummary, updatedTitle string
	gh := &MockGitHub{
		CreateCheckRunFn: func(_ context.Context, _, _, _ string, _ string) (int64, error) {
			return 99, nil
		},
		UpdateCheckRunFn: func(_ context.Context, _ string, id int64, u CheckRunUpdate) error {
			updatedID = id
			updatedSummary = u.Summary
			updatedTitle = u.Title
			return nil
		},
	}
	a := New(db, gh, Config{PublicBaseURL: "https://serve.example"})

	initBody, _ := json.Marshal(events.Init{
		ID: "apply-2", Repo: "o/r", SHA: "abc", PR: 0, Environment: "nonprod",
		Context: "apply/nonprod",
		Stacks:  []events.StackState{{Path: "a", Status: events.StatusPending}},
	})
	rec := httptest.NewRecorder()
	a.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/init", bytes.NewReader(initBody)))
	if rec.Code != http.StatusOK {
		t.Fatalf("init: %d", rec.Code)
	}

	phaseBody, _ := json.Marshal(events.PhaseEvent{ID: "apply-2", Phase: events.PhaseApplying})
	rec = httptest.NewRecorder()
	a.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/phase", bytes.NewReader(phaseBody)))
	if rec.Code != http.StatusOK {
		t.Fatalf("phase: %d", rec.Code)
	}

	updBody, _ := json.Marshal(events.Update{ID: "apply-2", Stack: "a", Status: events.StatusSafe})
	rec = httptest.NewRecorder()
	a.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/update", bytes.NewReader(updBody)))
	if rec.Code != http.StatusOK {
		t.Fatalf("update: %d", rec.Code)
	}

	if updatedID != 99 {
		t.Fatalf("UpdateCheckRun not called with ID 99 (got %d)", updatedID)
	}
	// Terminal apply: title shows count summary, not a frozen progress bar.
	if strings.Contains(updatedTitle, "▰") || strings.Contains(updatedTitle, "▱") {
		t.Errorf("terminal apply title must not contain the progress bar: %q", updatedTitle)
	}
	if !strings.Contains(updatedTitle, "applied") {
		t.Errorf("terminal apply title missing 'applied' count in: %q", updatedTitle)
	}
	// The applied count is now in the check-run title (not the summary);
	// the summary omits the headline to avoid duplication.
	if strings.HasPrefix(updatedSummary, "## ") {
		t.Errorf("summary must not start with a ## headline; got:\n%s", updatedSummary)
	}
	if !strings.Contains(updatedTitle, "applied") {
		t.Errorf("title missing applied count in: %q", updatedTitle)
	}
}

// TestApplyDriveFailureSurfacesStackAndNextSteps asserts a failed apply stack is
// attributed in the check run: the Summary carries a failure verdict + next-steps
// ("re-run"), and the Text shows the failing stack, its phase detail, and a
// per-stack log deep-link.
func TestApplyDriveFailureSurfacesStackAndNextSteps(t *testing.T) {
	db := newServerTestDB(t)
	var conclusion, summary, text string
	gh := &MockGitHub{
		CreateCheckRunFn: func(_ context.Context, _, _, _ string, _ string) (int64, error) { return 5, nil },
		UpdateCheckRunFn: func(_ context.Context, _ string, _ int64, u CheckRunUpdate) error {
			conclusion, summary, text = u.Conclusion, u.Summary, u.Text
			return nil
		},
	}
	a := New(db, gh, Config{PublicBaseURL: "https://serve.example"})

	initBody, _ := json.Marshal(events.Init{
		ID: "apply-fail", Repo: "o/r", SHA: "abc", PR: 0, Environment: "prod",
		Context: "apply/prod",
		Stacks:  []events.StackState{{Path: "cluster/fh-prod", Status: events.StatusPending}},
	})
	rec := httptest.NewRecorder()
	a.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/init", bytes.NewReader(initBody)))
	if rec.Code != http.StatusOK {
		t.Fatalf("init: %d", rec.Code)
	}

	updBody, _ := json.Marshal(events.Update{ID: "apply-fail", Stack: "cluster/fh-prod", Status: events.StatusFailed, Detail: "terraform apply failed"})
	rec = httptest.NewRecorder()
	a.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/update", bytes.NewReader(updBody)))
	if rec.Code != http.StatusOK {
		t.Fatalf("update: %d", rec.Code)
	}

	if conclusion != "failure" {
		t.Errorf("conclusion = %q, want failure", conclusion)
	}
	if !strings.Contains(strings.ToLower(summary), "re-run") {
		t.Errorf("summary missing next-steps guidance: %q", summary)
	}
	for _, want := range []string{"cluster/fh-prod", "terraform apply failed", "/logs/apply-fail/cluster/fh-prod"} {
		if !strings.Contains(text, want) {
			t.Errorf("text missing %q in:\n%s", want, text)
		}
	}
}

// A no-op apply (zero changed stacks — e.g. a docs/CI-only merge, or a PR whose
// only work was a cross-state move done in the pre-phase) must resolve the apply
// check run to a success conclusion on init, not hang forever at in_progress:
// nothing emits a stack-completion event to flip it terminal.
func TestApplyInitZeroStacksResolvesSuccess(t *testing.T) {
	db := newServerTestDB(t)
	var conclusion string
	gh := &MockGitHub{
		CreateCheckRunFn: func(_ context.Context, _, _, _ string, _ string) (int64, error) {
			return 7, nil
		},
		UpdateCheckRunFn: func(_ context.Context, _ string, _ int64, u CheckRunUpdate) error {
			conclusion = u.Conclusion
			return nil
		},
		PostStatusFn: func(_ context.Context, _, _, _, _, _, _ string) error {
			t.Error("PostStatus must not be called when a check run exists")
			return nil
		},
	}
	a := New(db, gh, Config{PublicBaseURL: "https://serve.example"})

	body, _ := json.Marshal(events.Init{
		ID: "apply-noop", Repo: "o/r", SHA: "abc", PR: 9, Environment: "nonprod",
		Context: "apply/nonprod",
		Stacks:  []events.StackState{}, // no changed stacks
	})
	rec := httptest.NewRecorder()
	a.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/init", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("init: %d %s", rec.Code, rec.Body.String())
	}
	if conclusion != "success" {
		t.Fatalf("zero-stack apply check run conclusion = %q, want success", conclusion)
	}
}

// TestApplyDrivePersistsSuccessStatus asserts that driveApply writes
// "success" into executions.status when all stacks complete safely so the
// viewer's isFinished() flips to true and the shimmer stops.
func TestApplyDrivePersistsSuccessStatus(t *testing.T) {
	db := newServerTestDB(t)
	gh := &MockGitHub{
		CreateCheckRunFn: func(_ context.Context, _, _, _ string, _ string) (int64, error) {
			return 11, nil
		},
		UpdateCheckRunFn: func(_ context.Context, _ string, _ int64, _ CheckRunUpdate) error {
			return nil
		},
	}
	a := New(db, gh, Config{PublicBaseURL: "https://serve.example"})

	initBody, _ := json.Marshal(events.Init{
		ID: "apply-persist-ok", Repo: "o/r", SHA: "abc", PR: 0, Environment: "nonprod",
		Context: "apply/nonprod",
		Stacks:  []events.StackState{{Path: "a", Status: events.StatusPending}},
	})
	rec := httptest.NewRecorder()
	a.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/init", bytes.NewReader(initBody)))
	if rec.Code != http.StatusOK {
		t.Fatalf("init: %d", rec.Code)
	}

	updBody, _ := json.Marshal(events.Update{ID: "apply-persist-ok", Stack: "a", Status: events.StatusSafe})
	rec = httptest.NewRecorder()
	a.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/update", bytes.NewReader(updBody)))
	if rec.Code != http.StatusOK {
		t.Fatalf("update: %d", rec.Code)
	}

	e, err := store.GetExecution(db, "apply-persist-ok")
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if e.Status != "success" {
		t.Errorf("execution status = %q, want success", e.Status)
	}
}

// TestApplyDrivePersistsFailureStatus asserts that driveApply writes
// "failure" into executions.status when a stack fails so the viewer's
// isFinished() flips to true and the shimmer stops.
func TestApplyDrivePersistsFailureStatus(t *testing.T) {
	db := newServerTestDB(t)
	gh := &MockGitHub{
		CreateCheckRunFn: func(_ context.Context, _, _, _ string, _ string) (int64, error) {
			return 12, nil
		},
		UpdateCheckRunFn: func(_ context.Context, _ string, _ int64, _ CheckRunUpdate) error {
			return nil
		},
	}
	a := New(db, gh, Config{PublicBaseURL: "https://serve.example"})

	initBody, _ := json.Marshal(events.Init{
		ID: "apply-persist-fail", Repo: "o/r", SHA: "abc", PR: 0, Environment: "prod",
		Context: "apply/prod",
		Stacks:  []events.StackState{{Path: "b", Status: events.StatusPending}},
	})
	rec := httptest.NewRecorder()
	a.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/init", bytes.NewReader(initBody)))
	if rec.Code != http.StatusOK {
		t.Fatalf("init: %d", rec.Code)
	}

	updBody, _ := json.Marshal(events.Update{ID: "apply-persist-fail", Stack: "b", Status: events.StatusFailed, Detail: "boom"})
	rec = httptest.NewRecorder()
	a.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/update", bytes.NewReader(updBody)))
	if rec.Code != http.StatusOK {
		t.Fatalf("update: %d", rec.Code)
	}

	e, err := store.GetExecution(db, "apply-persist-fail")
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if e.Status != "failure" {
		t.Errorf("execution status = %q, want failure", e.Status)
	}
}

// TestDriveApplyVerdict checks three verdict scenarios:
//   - all stacks applied/nochange (both count as applied) → success conclusion
//   - failure flag set on execution + all stacks aborted → failure conclusion
//   - one stack failed → failure conclusion
//
// Harness mirrors checkrun_apply_test.go: newServerTestDB + MockGitHub +
// store.UpsertInit with terminal stack statuses + store.SetCheckRunID + driveApply.
func TestDriveApplyVerdict(t *testing.T) {
	cases := []struct {
		name      string
		execState string
		stacks    []events.Status
		wantConc  string
	}{
		{"all applied/nochange", "", []events.Status{events.StatusSafe, events.StatusNochange}, "success"},
		{"failure flag, none failed-per-stack", "failure", []events.Status{events.StatusAborted, events.StatusAborted}, "failure"},
		{"one failed", "", []events.Status{events.StatusFailed, events.StatusSafe}, "failure"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := newServerTestDB(t)
			var gotConclusion string
			gh := &MockGitHub{
				CreateCheckRunFn: func(_ context.Context, _, _, _ string, _ string) (int64, error) {
					return 77, nil
				},
				UpdateCheckRunFn: func(_ context.Context, _ string, _ int64, u CheckRunUpdate) error {
					gotConclusion = u.Conclusion
					return nil
				},
			}
			a := New(db, gh, Config{PublicBaseURL: "https://serve.example"})

			id := "verdict-" + c.name
			ss := make([]events.StackState, len(c.stacks))
			for i, st := range c.stacks {
				ss[i] = events.StackState{Path: fmt.Sprintf("s%d", i), Status: st}
			}
			if err := store.UpsertInit(db, events.Init{
				ID:          id,
				Repo:        "o/r",
				Context:     "apply/nonprod",
				Environment: "nonprod",
				Stacks:      ss,
			}); err != nil {
				t.Fatalf("UpsertInit: %v", err)
			}
			if err := store.SetCheckRunID(db, id, 77); err != nil {
				t.Fatalf("SetCheckRunID: %v", err)
			}
			if c.execState != "" {
				if err := store.SetExecutionStatus(db, id, c.execState); err != nil {
					t.Fatalf("SetExecutionStatus: %v", err)
				}
			}
			e, err := store.GetExecution(db, id)
			if err != nil {
				t.Fatalf("GetExecution: %v", err)
			}
			a.driveApply(context.Background(), e, "http://x")
			if gotConclusion != c.wantConc {
				t.Errorf("conclusion = %q, want %q", gotConclusion, c.wantConc)
			}
		})
	}
}
