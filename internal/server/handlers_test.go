package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// post marshals v and POSTs it to the test server path, returning the status code.
func post(t *testing.T, srv *httptest.Server, path string, v any) int {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(srv.URL+path, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestInitCreatesExecutionAndCheckRunOnce(t *testing.T) {
	db := newServerTestDB(t)
	gh := &MockGitHub{CreateCheckRunFn: func(ctx context.Context, repo, sha, env, url string) (int64, error) { return 555, nil }}
	a := New(db, gh, Config{UseChecks: true})
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	in := events.Init{ID: "e1", Repo: "o/r", SHA: "sha", PR: 7, Environment: "staging",
		Stacks: []events.StackState{{Path: "a"}, {Path: "b"}}, Edges: []events.Edge{{From: "a", To: "b"}}}

	if code := post(t, srv, "/api/phase", events.PhaseEvent{ID: "e1", Repo: "o/r", SHA: "sha", PR: 7, Environment: "staging", Phase: events.PhaseWarming}); code != 200 {
		t.Fatalf("phase = %d", code)
	}
	if code := post(t, srv, "/api/init", in); code != 200 {
		t.Fatalf("init = %d", code)
	}
	if gh.CreateCheckRunCalls != 1 {
		t.Fatalf("CreateCheckRunCalls = %d, want 1 (idempotent across phase+init)", gh.CreateCheckRunCalls)
	}
	e, err := store.GetExecution(db, "e1")
	if err != nil {
		t.Fatal(err)
	}
	if !e.CheckRunID.Valid || e.CheckRunID.Int64 != 555 || e.Phase != string(events.PhaseWarming) {
		t.Fatalf("execution = %+v", e)
	}
}

func TestUpdateTicksStackAndPatches(t *testing.T) {
	db := newServerTestDB(t)
	var mu sync.Mutex
	var lastUpd CheckRunUpdate
	gh := &MockGitHub{
		CreateCheckRunFn: func(ctx context.Context, repo, sha, env, url string) (int64, error) { return 1, nil },
		UpdateCheckRunFn: func(ctx context.Context, repo string, id int64, u CheckRunUpdate) error {
			mu.Lock()
			lastUpd = u
			mu.Unlock()
			return nil
		},
	}
	a := New(db, gh, Config{UseChecks: true})
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	post(t, srv, "/api/init", events.Init{ID: "e1", Repo: "o/r", SHA: "sha", PR: 7, Environment: "staging",
		Stacks: []events.StackState{{Path: "a"}}})
	if code := post(t, srv, "/api/update", events.Update{ID: "e1", Stack: "a", Status: events.StatusRunning}); code != 200 {
		t.Fatalf("update = %d", code)
	}
	g, _ := store.LoadGraph(db, "e1")
	if g.Stacks[0].Status != events.StatusRunning {
		t.Fatalf("stack a = %q, want running", g.Stacks[0].Status)
	}
	mu.Lock()
	defer mu.Unlock()
	if lastUpd.Summary == "" {
		t.Fatal("expected a non-empty check-run summary after update")
	}
}

func TestLinkModePostsStatus(t *testing.T) {
	db := newServerTestDB(t)
	var mu sync.Mutex
	var gotState, gotContext string
	gh := &MockGitHub{PostStatusFn: func(ctx context.Context, repo, sha, context_, state, desc, url string) error {
		mu.Lock()
		gotState, gotContext = state, context_
		mu.Unlock()
		return nil
	}}
	a := New(db, gh, Config{UseChecks: false})
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	post(t, srv, "/api/init", events.Init{ID: "e1", Repo: "o/r", SHA: "sha", PR: 7, Environment: "staging",
		Stacks: []events.StackState{{Path: "a"}}})
	if gh.CreateCheckRunCalls != 0 {
		t.Fatalf("link mode must not create a check run, got %d", gh.CreateCheckRunCalls)
	}
	mu.Lock()
	defer mu.Unlock()
	if gotContext != "plan/staging" || gotState != "pending" {
		t.Fatalf("status = %q/%q, want plan/staging/pending", gotContext, gotState)
	}
}

func TestFinalizeCleanPlanConcludesSuccess(t *testing.T) {
	db := newServerTestDB(t)
	var mu sync.Mutex
	var concl string
	gh := &MockGitHub{
		CreateCheckRunFn: func(ctx context.Context, repo, sha, env, url string) (int64, error) { return 1, nil },
		UpdateCheckRunFn: func(ctx context.Context, repo string, id int64, u CheckRunUpdate) error {
			mu.Lock()
			concl = u.Conclusion
			mu.Unlock()
			return nil
		},
	}
	a := New(db, gh, Config{UseChecks: true})
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()
	post(t, srv, "/api/init", events.Init{ID: "e1", Repo: "o/r", SHA: "sha", PR: 7, Environment: "staging",
		Stacks: []events.StackState{{Path: "a", Status: events.StatusPlanned}}})
	if code := post(t, srv, "/api/finalize", events.Finalize{ID: "e1", ReportMarkdown: "# report"}); code != 200 {
		t.Fatalf("finalize = %d", code)
	}
	mu.Lock()
	defer mu.Unlock()
	if concl != "success" {
		t.Fatalf("conclusion = %q, want success", concl)
	}
	if ok, _ := store.IsClassified(db, 7, "staging"); !ok {
		t.Fatal("expected classified marker")
	}
}

func TestFinalizeGatedPlanConcludesActionRequired(t *testing.T) {
	db := newServerTestDB(t)
	var mu sync.Mutex
	var concl string
	gh := &MockGitHub{
		CreateCheckRunFn: func(ctx context.Context, repo, sha, env, url string) (int64, error) { return 1, nil },
		UpdateCheckRunFn: func(ctx context.Context, repo string, id int64, u CheckRunUpdate) error {
			mu.Lock()
			concl = u.Conclusion
			mu.Unlock()
			return nil
		},
	}
	a := New(db, gh, Config{UseChecks: true})
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()
	post(t, srv, "/api/init", events.Init{ID: "e1", Repo: "o/r", SHA: "sha", PR: 7, Environment: "staging",
		Stacks: []events.StackState{{Path: "a", Project: "proj-a", Status: events.StatusPlanned}}})
	post(t, srv, "/api/finalize", events.Finalize{
		ID: "e1", ReportMarkdown: "# report",
		Gates: []events.GateTarget{{Class: "iam", Target: "proj-a"}},
	})
	mu.Lock()
	defer mu.Unlock()
	if concl != "action_required" {
		t.Fatalf("conclusion = %q, want action_required", concl)
	}
	g, _ := store.LoadGraph(db, "e1")
	if g.Stacks[0].Status != events.StatusGated {
		t.Errorf("stack a = %q, want gated", g.Stacks[0].Status)
	}
	ts, _ := store.TargetsFor(db, 7, "staging")
	if len(ts) != 1 || ts[0].State != "AWAITING" {
		t.Errorf("targets = %+v, want one AWAITING", ts)
	}
}

func TestFinalizeStoresPerStackPlan(t *testing.T) {
	db := newServerTestDB(t)
	a := New(db, &MockGitHub{}, Config{})
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	_ = store.UpsertInit(db, events.Init{ID: "e1", Repo: "o/r", Environment: "staging",
		Stacks: []events.StackState{{Path: "stacks/a"}}})
	post(t, srv, "/api/finalize", events.Finalize{
		ID:             "e1",
		ReportMarkdown: "combined",
		StackReports:   map[string]string{"stacks/a": "PLAN_A_SECTION"},
	})

	_, excerpt, ok, err := store.GetStackOutput(db, "e1", "stacks/a", "plan")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || excerpt != "PLAN_A_SECTION" {
		t.Fatalf("stored plan = %q ok=%v, want PLAN_A_SECTION", excerpt, ok)
	}
}

func TestFinalizeFailedMarksRunningStacksFailed(t *testing.T) {
	db := newServerTestDB(t)
	gh := &MockGitHub{CreateCheckRunFn: func(ctx context.Context, repo, sha, env, url string) (int64, error) { return 1, nil }}
	a := New(db, gh, Config{UseChecks: true})
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()
	post(t, srv, "/api/init", events.Init{ID: "e1", Repo: "o/r", SHA: "sha", PR: 7, Environment: "staging",
		Stacks: []events.StackState{{Path: "a", Status: events.StatusRunning}, {Path: "b", Status: events.StatusPlanned}}})
	post(t, srv, "/api/finalize", events.Finalize{ID: "e1", ReportMarkdown: "# report", Failed: true})
	g, _ := store.LoadGraph(db, "e1")
	var aStatus, bStatus events.Status
	for _, s := range g.Stacks {
		if s.Path == "a" {
			aStatus = s.Status
		}
		if s.Path == "b" {
			bStatus = s.Status
		}
	}
	if aStatus != events.StatusFailed || bStatus != events.StatusPlanned {
		t.Fatalf("a=%q b=%q, want failed/planned", aStatus, bStatus)
	}
}
