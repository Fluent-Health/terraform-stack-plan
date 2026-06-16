package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
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
	a := New(db, gh, Config{UseChecks: true, PublicBaseURL: "https://serve.example"})

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
	var updatedSummary string
	gh := &MockGitHub{
		CreateCheckRunFn: func(_ context.Context, _, _, _ string, _ string) (int64, error) {
			return 99, nil
		},
		UpdateCheckRunFn: func(_ context.Context, _ string, id int64, u CheckRunUpdate) error {
			updatedID = id
			updatedSummary = u.Summary
			return nil
		},
	}
	a := New(db, gh, Config{UseChecks: true, PublicBaseURL: "https://serve.example"})

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

	updBody, _ := json.Marshal(events.Update{ID: "apply-2", Stack: "a", Status: events.StatusSafe})
	rec = httptest.NewRecorder()
	a.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/update", bytes.NewReader(updBody)))
	if rec.Code != http.StatusOK {
		t.Fatalf("update: %d", rec.Code)
	}

	if updatedID != 99 {
		t.Fatalf("UpdateCheckRun not called with ID 99 (got %d)", updatedID)
	}
	if !strings.Contains(updatedSummary, "1") {
		t.Fatalf("summary missing stack count: %q", updatedSummary)
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
	a := New(db, gh, Config{UseChecks: true, PublicBaseURL: "https://serve.example"})

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
	a := New(db, gh, Config{UseChecks: true, PublicBaseURL: "https://serve.example"})

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
