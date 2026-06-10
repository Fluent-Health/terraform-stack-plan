package server

import (
	"context"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

func TestE2EPlanLifecycle(t *testing.T) {
	db := newServerTestDB(t)
	var mu sync.Mutex
	updates := 0
	var finalConcl string
	gh := &MockGitHub{
		CreateCheckRunFn: func(ctx context.Context, repo, sha, env, url string) (int64, error) { return 42, nil },
		UpdateCheckRunFn: func(ctx context.Context, repo string, id int64, u CheckRunUpdate) error {
			mu.Lock()
			updates++
			if u.Conclusion != "" {
				finalConcl = u.Conclusion
			}
			mu.Unlock()
			return nil
		},
	}
	a := New(db, gh, Config{UseChecks: true, PublicBaseURL: "https://srv"})
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	post(t, srv, "/api/phase", events.PhaseEvent{ID: "e1", Repo: "o/r", SHA: "sha", PR: 7, Environment: "staging", Phase: events.PhaseWarming})
	post(t, srv, "/api/init", events.Init{ID: "e1", Repo: "o/r", SHA: "sha", PR: 7, Environment: "staging",
		Stacks: []events.StackState{{Path: "a"}, {Path: "b"}}, Edges: []events.Edge{{From: "a", To: "b"}}})
	post(t, srv, "/api/update", events.Update{ID: "e1", Stack: "a", Status: events.StatusRunning})
	post(t, srv, "/api/update", events.Update{ID: "e1", Stack: "a", Status: events.StatusPlanned})
	post(t, srv, "/api/update", events.Update{ID: "e1", Stack: "b", Status: events.StatusPlanned})
	post(t, srv, "/api/finalize", events.Finalize{ID: "e1", ReportMarkdown: "# report"})

	if gh.CreateCheckRunCalls != 1 {
		t.Fatalf("CreateCheckRunCalls = %d, want 1", gh.CreateCheckRunCalls)
	}
	mu.Lock()
	defer mu.Unlock()
	if finalConcl != "success" {
		t.Fatalf("final conclusion = %q, want success", finalConcl)
	}
	if updates < 2 {
		t.Fatalf("expected several check-run patches, got %d", updates)
	}

	e, err := store.GetExecution(db, "e1")
	if err != nil {
		t.Fatal(err)
	}
	if e.ReportMarkdown != "# report" || e.Rev == 0 {
		t.Fatalf("execution = %+v", e)
	}
	_ = context.Background
}
