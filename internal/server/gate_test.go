package server

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// newGatedExecution inits a gated plan (one stack on proj-a) and finalizes it
// with an iam gate on proj-a.
func newGatedExecution(t *testing.T, srv *httptest.Server) {
	t.Helper()
	post(t, srv, "/api/init", events.Init{ID: "e1", Repo: "o/r", SHA: "sha", PR: 7, Environment: "staging",
		Stacks: []events.StackState{{Path: "stacks/a", Project: "proj-a", Status: events.StatusPlanned}}})
	post(t, srv, "/api/finalize", events.Finalize{ID: "e1", ReportMarkdown: "# report",
		Gates: []events.GateTarget{{Class: "iam", Target: "proj-a"}}})
}

func TestFinalizeRequestsGrants(t *testing.T) {
	db := newServerTestDB(t)
	fake := approval.NewFake()
	a := New(db, &MockGitHub{}, Config{UseChecks: true})
	a.Approval = fake
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	newGatedExecution(t, srv)

	ts, _ := store.TargetsFor(db, 7, "staging")
	if len(ts) != 1 || ts[0].Target != "proj-a" || ts[0].State != "AWAITING" || ts[0].GrantName == "" {
		t.Fatalf("targets = %+v, want one AWAITING with a grant name", ts)
	}
	grants, _ := fake.ListGrants(context.Background(), "iam", "proj-a")
	if len(grants) != 1 {
		t.Fatalf("backend grants = %d, want 1", len(grants))
	}
}

func TestReconcileGateFlipsActiveOnApproval(t *testing.T) {
	db := newServerTestDB(t)
	fake := approval.NewFake()
	var concl string
	gh := &MockGitHub{
		CreateCheckRunFn: func(ctx context.Context, repo, sha, env, url string) (int64, error) { return 1, nil },
		UpdateCheckRunFn: func(ctx context.Context, repo string, id int64, u CheckRunUpdate) error {
			if u.Conclusion != "" {
				concl = u.Conclusion
			}
			return nil
		},
	}
	a := New(db, gh, Config{UseChecks: true})
	a.Approval = fake
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()
	newGatedExecution(t, srv)

	g, _ := store.LoadGraph(db, "e1")
	if g.Stacks[0].Status != events.StatusGated {
		t.Fatalf("stack = %s, want gated", g.Stacks[0].Status)
	}

	fake.Approve(approval.Request{Class: "iam", Target: "proj-a", PR: 7, Environment: "staging"})
	a.reconcileGate(context.Background(), 7, "staging")

	ts, _ := store.TargetsFor(db, 7, "staging")
	if ts[0].State != "ACTIVE" {
		t.Errorf("target state = %s, want ACTIVE after reconcile", ts[0].State)
	}
	g, _ = store.LoadGraph(db, "e1")
	if g.Stacks[0].Status != events.StatusSafe {
		t.Errorf("stack = %s, want safe after approval", g.Stacks[0].Status)
	}
	if concl != "success" {
		t.Errorf("check-run conclusion = %q, want success after approval", concl)
	}
}

func TestLatestExecutionID(t *testing.T) {
	db := newServerTestDB(t)
	_ = store.UpsertInit(db, events.Init{ID: "e1", Repo: "o/r", PR: 7, Environment: "staging"})
	id, ok := store.LatestExecutionID(db, 7, "staging")
	if !ok || id != "e1" {
		t.Fatalf("LatestExecutionID = %q, %v", id, ok)
	}
	if _, ok := store.LatestExecutionID(db, 99, "nope"); ok {
		t.Error("want ok=false for unknown pr/env")
	}
}

func TestGateCheckFailClosed(t *testing.T) {
	db := newServerTestDB(t)
	fake := approval.NewFake()
	a := New(db, &MockGitHub{}, Config{UseChecks: true})
	a.Approval = fake
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	if code := post(t, srv, "/api/gate/check", events.GateCheck{PR: 7, Environment: "staging"}); code != 409 {
		t.Fatalf("unplanned gate/check = %d, want 409", code)
	}

	newGatedExecution(t, srv)

	if code := post(t, srv, "/api/gate/check", events.GateCheck{PR: 7, Environment: "staging"}); code != 409 {
		t.Fatalf("gated/check before approval = %d, want 409", code)
	}

	fake.Approve(approval.Request{Class: "iam", Target: "proj-a", PR: 7, Environment: "staging"})
	if code := post(t, srv, "/api/gate/check", events.GateCheck{PR: 7, Environment: "staging"}); code != 200 {
		t.Fatalf("approved gate/check = %d, want 200", code)
	}
}

func TestGateCheckCleanPlanPasses(t *testing.T) {
	db := newServerTestDB(t)
	a := New(db, &MockGitHub{}, Config{})
	a.Approval = approval.NewFake()
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()
	post(t, srv, "/api/init", events.Init{ID: "e1", Repo: "o/r", PR: 8, Environment: "staging",
		Stacks: []events.StackState{{Path: "a", Status: events.StatusPlanned}}})
	post(t, srv, "/api/finalize", events.Finalize{ID: "e1", ReportMarkdown: "# report"})
	if code := post(t, srv, "/api/gate/check", events.GateCheck{PR: 8, Environment: "staging"}); code != 200 {
		t.Fatalf("clean plan gate/check = %d, want 200", code)
	}
}

func TestGateRevoke(t *testing.T) {
	db := newServerTestDB(t)
	fake := approval.NewFake()
	a := New(db, &MockGitHub{}, Config{UseChecks: true})
	a.Approval = fake
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()
	newGatedExecution(t, srv)
	fake.Approve(approval.Request{Class: "iam", Target: "proj-a", PR: 7, Environment: "staging"})

	if code := post(t, srv, "/api/gate/revoke", events.GateRevoke{PR: 7, Environment: "staging"}); code != 200 {
		t.Fatalf("gate/revoke = %d, want 200", code)
	}
	grants, _ := fake.ListGrants(context.Background(), "iam", "proj-a")
	if grants[0].State != approval.StateRevoked {
		t.Errorf("grant state after revoke = %s, want REVOKED", grants[0].State)
	}
}
