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
