package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// postResp marshals v and POSTs it to the test server path, returning the full response.
func postResp(t *testing.T, srv *httptest.Server, path string, v any) *http.Response {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(srv.URL+path, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

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

// TestRequestGrantsSharedRequester verifies that requestGrants leases ONE
// requester from the pool on the first grant and pins it on every subsequent
// gate of the same PR — both store rows must share the same non-empty requester.
func TestRequestGrantsSharedRequester(t *testing.T) {
	db := newServerTestDB(t)
	fake := approval.NewFake()
	fake.Pool = []string{"sa0@project.iam.gserviceaccount.com", "sa1@project.iam.gserviceaccount.com"}
	a := New(db, &MockGitHub{}, Config{})
	a.Approval = fake

	gates := []events.GateTarget{
		{Class: "iam", Target: "proj-1"},
		{Class: "iam", Target: "proj-2"},
	}
	a.requestGrants(context.Background(), 7, "nonprod", gates)

	ts, err := store.TargetsFor(db, 7, "nonprod")
	if err != nil {
		t.Fatalf("TargetsFor: %v", err)
	}
	if len(ts) != 2 {
		t.Fatalf("want 2 targets, got %d", len(ts))
	}
	if ts[0].Requester == "" {
		t.Fatalf("ts[0].Requester is empty, want a leased SA")
	}
	if ts[0].Requester != ts[1].Requester {
		t.Errorf("requesters differ: ts[0]=%q ts[1]=%q — want shared across gates", ts[0].Requester, ts[1].Requester)
	}
}

// TestGateCheckReturnsRequester verifies that after approval, gate/check returns
// 200 with a JSON body containing the leased requester SA.
func TestGateCheckReturnsRequester(t *testing.T) {
	db := newServerTestDB(t)
	fake := approval.NewFake()
	fake.Pool = []string{"sa0@project.iam.gserviceaccount.com"}
	a := New(db, &MockGitHub{}, Config{UseChecks: true})
	a.Approval = fake
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	newGatedExecution(t, srv)
	fake.Approve(approval.Request{Class: "iam", Target: "proj-a", PR: 7, Environment: "staging"})

	resp := postResp(t, srv, "/api/gate/check", events.GateCheck{PR: 7, Environment: "staging"})
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("gate/check = %d, want 200", resp.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	want := "sa0@project.iam.gserviceaccount.com"
	if body["requester"] != want {
		t.Errorf("body[requester] = %q, want %q", body["requester"], want)
	}
}
