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
	a := New(db, gh, Config{UseChecks: true, PublicBaseURL: "https://serve.example"})

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

	for _, want := range []string{"▰", "1/1", "planned"} {
		if !strings.Contains(title, want) {
			t.Errorf("title missing %q in: %q", want, title)
		}
	}
	for _, want := range []string{"## Plan · nonprod", "| Stack | Ops | Risk | State |", "`svc/a`"} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary missing %q in:\n%s", want, summary)
		}
	}
}
