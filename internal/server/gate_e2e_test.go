package server

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"net/http/httptest"

	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

func TestGatedLifecycleE2E(t *testing.T) {
	db := newServerTestDB(t)
	fake := approval.NewFake()
	fake.Pool = []string{"sa0", "sa1"}
	var mu sync.Mutex
	var concl string
	getConcl := func() string { mu.Lock(); defer mu.Unlock(); return concl }
	gh := &MockGitHub{
		CreateCheckRunFn: func(ctx context.Context, repo, sha, env, url string) (int64, error) { return 1, nil },
		UpdateCheckRunFn: func(ctx context.Context, repo string, id int64, u CheckRunUpdate) error {
			// Ignore the apply-lock/<env> check (always-on); this test asserts on the
			// plan/<env> gate check-run conclusion only.
			if strings.HasPrefix(u.Title, "apply-lock") {
				return nil
			}
			if u.Conclusion != "" {
				mu.Lock()
				concl = u.Conclusion
				mu.Unlock()
			}
			return nil
		},
	}
	a := New(db, gh, Config{})
	a.Approval = fake
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	post(t, srv, "/api/init", events.Init{ID: "e1", Repo: "o/r", SHA: "sha", PR: 7, Environment: "staging",
		Stacks: []events.StackState{{Path: "stacks/a", Project: "proj-a", Status: events.StatusPlanned}}})
	post(t, srv, "/api/finalize", events.Finalize{ID: "e1", ReportMarkdown: "# report",
		Gates: []events.GateTarget{{Class: "iam", Target: "proj-a"}}})

	// Gated finalize completes the check run with action_required.
	if getConcl() != "action_required" {
		t.Fatalf("gated plan conclusion = %q, want action_required", getConcl())
	}
	// Stack overlay: proj-a stack is gated.
	if got := stackStatus(t, &Shell{app: a}, "e1", "stacks/a"); got != "gated" {
		t.Fatalf("stack overlay before approval = %q, want gated", got)
	}
	// Apply gate is closed before approval.
	if code := post(t, srv, "/api/gate/check", events.GateCheck{PR: 7, Environment: "staging"}); code != 409 {
		t.Fatalf("gate/check before approval = %d, want 409", code)
	}

	// Approver flips the grant ACTIVE; the reconcile loop converges it.
	fake.Approve(approval.Request{Class: "iam", Target: "proj-a", PR: 7, Environment: "staging"})
	ctx, cancel := context.WithCancel(context.Background())
	go a.ReconcileLoop(ctx, 10*time.Millisecond)
	defer cancel()

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

	// After approval: apply gate opens with the leased requester, and the stack
	// overlay flips to safe.
	if code := post(t, srv, "/api/gate/check", events.GateCheck{PR: 7, Environment: "staging"}); code != 200 {
		t.Errorf("gate/check after approval = %d, want 200", code)
	}
	if got := stackStatus(t, &Shell{app: a}, "e1", "stacks/a"); got != "safe" {
		t.Errorf("stack overlay after approval = %q, want safe", got)
	}
	if code := post(t, srv, "/api/gate/revoke", events.GateRevoke{PR: 7, Environment: "staging"}); code != 200 {
		t.Errorf("gate/revoke = %d, want 200", code)
	}
}
