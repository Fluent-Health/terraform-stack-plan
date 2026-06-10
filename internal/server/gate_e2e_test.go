package server

import (
	"context"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

func TestGatedLifecycleE2E(t *testing.T) {
	db := newServerTestDB(t)
	fake := approval.NewFake()
	// mu guards concl: UpdateCheckRunFn is called from both the ReconcileLoop
	// goroutine and the HTTP-server goroutine that handles gate/check (which
	// reconciles inline), so unsynchronised access is a data race.
	var mu sync.Mutex
	var concl string
	getConcl := func() string { mu.Lock(); defer mu.Unlock(); return concl }
	gh := &MockGitHub{
		CreateCheckRunFn: func(ctx context.Context, repo, sha, env, url string) (int64, error) { return 1, nil },
		UpdateCheckRunFn: func(ctx context.Context, repo string, id int64, u CheckRunUpdate) error {
			if u.Conclusion != "" {
				mu.Lock()
				concl = u.Conclusion
				mu.Unlock()
			}
			return nil
		},
	}
	a := New(db, gh, Config{UseChecks: true})
	a.Approval = fake
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	post(t, srv, "/api/init", events.Init{ID: "e1", Repo: "o/r", SHA: "sha", PR: 7, Environment: "staging",
		Stacks: []events.StackState{{Path: "stacks/a", Project: "proj-a", Status: events.StatusPlanned}}})
	post(t, srv, "/api/finalize", events.Finalize{ID: "e1", ReportMarkdown: "# report",
		Gates: []events.GateTarget{{Class: "iam", Target: "proj-a"}}})
	if getConcl() != "action_required" {
		t.Fatalf("gated plan conclusion = %q, want action_required", getConcl())
	}
	if code := post(t, srv, "/api/gate/check", events.GateCheck{PR: 7, Environment: "staging"}); code != 409 {
		t.Fatalf("gate/check before approval = %d, want 409", code)
	}

	fake.Approve(approval.Request{Class: "iam", Target: "proj-a", PR: 7, Environment: "staging"})
	ctx, cancel := context.WithCancel(context.Background())
	go a.ReconcileLoop(ctx, 10*time.Millisecond)
	defer cancel()

	// Wait for both the DB gate to go ACTIVE and the check-run conclusion to flip
	// to success. drive() runs after UpsertTarget in the same reconcileGate call,
	// so we poll both to avoid a TOCTOU window between the DB write and the
	// UpdateCheckRun callback.
	deadline := time.After(2 * time.Second)
	for {
		ts, _ := store.TargetsFor(db, 7, "staging")
		if len(ts) == 1 && ts[0].State == "ACTIVE" && getConcl() == "success" {
			break
		}
		select {
		case <-deadline:
			t.Fatal("gate did not converge to ACTIVE within 2s")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()

	if getConcl() != "success" {
		t.Errorf("conclusion after approval = %q, want success", getConcl())
	}
	if code := post(t, srv, "/api/gate/check", events.GateCheck{PR: 7, Environment: "staging"}); code != 200 {
		t.Errorf("gate/check after approval = %d, want 200", code)
	}
	if code := post(t, srv, "/api/gate/revoke", events.GateRevoke{PR: 7, Environment: "staging"}); code != 200 {
		t.Errorf("gate/revoke = %d, want 200", code)
	}
}
