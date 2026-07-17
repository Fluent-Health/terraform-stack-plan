package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
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
	a := New(db, gh, Config{})
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
	a := New(db, gh, Config{})
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

func TestFinalizeCleanPlanConcludesSuccess(t *testing.T) {
	db := newServerTestDB(t)
	var mu sync.Mutex
	var concl string
	gh := &MockGitHub{
		CreateCheckRunFn: func(ctx context.Context, repo, sha, env, url string) (int64, error) { return 1, nil },
		UpdateCheckRunFn: func(ctx context.Context, repo string, id int64, u CheckRunUpdate) error {
			// Ignore the always-on apply-lock/<env> check; assert on the gate check only.
			if strings.HasPrefix(u.Title, "apply-lock") {
				return nil
			}
			mu.Lock()
			concl = u.Conclusion
			mu.Unlock()
			return nil
		},
	}
	a := New(db, gh, Config{})
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
}

func TestFinalizeGatedPlanStaysPending(t *testing.T) {
	db := newServerTestDB(t)
	var mu sync.Mutex
	var concl, title string
	gh := &MockGitHub{
		CreateCheckRunFn: func(ctx context.Context, repo, sha, env, url string) (int64, error) { return 1, nil },
		UpdateCheckRunFn: func(ctx context.Context, repo string, id int64, u CheckRunUpdate) error {
			// Ignore the always-on apply-lock/<env> check; assert on the gate check only.
			if strings.HasPrefix(u.Title, "apply-lock") {
				return nil
			}
			mu.Lock()
			concl = u.Conclusion
			title = u.Title
			mu.Unlock()
			return nil
		},
	}
	a := New(db, gh, Config{})
	a.Approval = approval.NewFake()
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
	// Awaiting a human keeps the check in_progress (pending, not the red
	// action_required); the title names the wait.
	if concl != "" {
		t.Fatalf("conclusion = %q, want \"\" (in_progress)", concl)
	}
	if title != "awaiting approval — 0 of 1 gates active" {
		t.Fatalf("title = %q, want awaiting-approval title", title)
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

// A non-failed finalize is terminal for plan and verify runs — the execution
// row must reach status success, or it reads as in_progress forever (lifecycle
// bar stuck at "report · now", PRs list stuck "planning"). Apply-context
// finalizes are NOT terminal (the classify pass emits one mid-apply) and must
// not be concluded here.
func TestFinalizeSetsTerminalStatusForPlanAndVerify(t *testing.T) {
	db := newServerTestDB(t)
	a := New(db, &MockGitHub{}, Config{})
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	post(t, srv, "/api/init", events.Init{ID: "p1", Repo: "o/r", SHA: "s", PR: 7, Environment: "staging",
		Context: "plan/staging", Stacks: []events.StackState{{Path: "a", Status: events.StatusPlanned}}})
	post(t, srv, "/api/finalize", events.Finalize{ID: "p1", ReportMarkdown: "# report"})
	if e, err := store.GetExecution(db, "p1"); err != nil || e.Status != "success" {
		t.Errorf("plan status = %q (err %v); want success", e.Status, err)
	}

	post(t, srv, "/api/init", events.Init{ID: "v1", Repo: "o/r", SHA: "s", PR: 7, Environment: "staging",
		Context: "verify/staging", Stacks: []events.StackState{{Path: "a", Status: events.StatusPending}}})
	post(t, srv, "/api/finalize", events.Finalize{ID: "v1", ReportMarkdown: "✅"})
	if e, err := store.GetExecution(db, "v1"); err != nil || e.Status != "success" {
		t.Errorf("verify status = %q (err %v); want success", e.Status, err)
	}

	post(t, srv, "/api/init", events.Init{ID: "ap1", Repo: "o/r", SHA: "s", PR: 7, Environment: "staging",
		Context: "apply/staging", Stacks: []events.StackState{{Path: "a", Status: events.StatusPending}}})
	post(t, srv, "/api/finalize", events.Finalize{ID: "ap1", ReportMarkdown: "classify pass"})
	if e, err := store.GetExecution(db, "ap1"); err == nil && e.Status == "success" {
		t.Errorf("apply status = success after mid-apply finalize; must stay non-terminal")
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

func TestFinalizeStoresCategories(t *testing.T) {
	db := newServerTestDB(t)
	a := New(db, &MockGitHub{}, Config{})
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	_ = store.UpsertInit(db, events.Init{ID: "e1", Repo: "o/r", Environment: "staging",
		Stacks: []events.StackState{{Path: "stacks/a"}}})
	post(t, srv, "/api/finalize", events.Finalize{
		ID:             "e1",
		ReportMarkdown: "r",
		Categories:     map[string][]events.Category{"stacks/a": {{Name: "iam", Icon: "🔐"}, {Name: "destructive", Icon: "💣"}}},
	})

	g, err := store.LoadGraph(db, "e1")
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Stacks) != 1 || len(g.Stacks[0].Categories) != 2 || g.Stacks[0].Categories[0].Name != "iam" {
		t.Fatalf("categories not persisted: %+v", g.Stacks)
	}
}

func TestFinalizeBackfillsCounts(t *testing.T) {
	db := newServerTestDB(t)
	a := New(db, &MockGitHub{}, Config{})
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	_ = store.UpsertInit(db, events.Init{ID: "e1", Repo: "o/r", Environment: "staging",
		Stacks: []events.StackState{{Path: "stacks/a"}}})
	post(t, srv, "/api/finalize", events.Finalize{
		ID:             "e1",
		ReportMarkdown: "r",
		Counts:         map[string]events.Counts{"stacks/a": {Add: 6}},
	})

	g, err := store.LoadGraph(db, "e1")
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Stacks) != 1 || g.Stacks[0].Counts == nil || g.Stacks[0].Counts.Add != 6 {
		t.Fatalf("counts not persisted: %+v", g.Stacks)
	}
}

func TestFinalizeFailedMarksRunningStacksAborted(t *testing.T) {
	db := newServerTestDB(t)
	gh := &MockGitHub{CreateCheckRunFn: func(ctx context.Context, repo, sha, env, url string) (int64, error) { return 1, nil }}
	a := New(db, gh, Config{})
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()
	// Stack "a" is running (non-terminal) — will become aborted.
	// Stack "b" is planned (already terminal) — must not be changed.
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
	if aStatus != events.StatusAborted || bStatus != events.StatusPlanned {
		t.Fatalf("a=%q b=%q, want aborted/planned", aStatus, bStatus)
	}
}

func TestFinalizeFailedMarksNonTerminalAborted(t *testing.T) {
	db := newServerTestDB(t)
	gh := &MockGitHub{CreateCheckRunFn: func(ctx context.Context, repo, sha, env, url string) (int64, error) { return 1, nil }}
	a := New(db, gh, Config{})
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	// Init with two running stacks; tick stack "a" to failed before finalize.
	post(t, srv, "/api/init", events.Init{
		ID: "exec-abort", Repo: "o/r", SHA: "sha", PR: 9, Environment: "staging",
		Stacks: []events.StackState{
			{Path: "a", Status: events.StatusRunning},
			{Path: "b", Status: events.StatusRunning},
		},
	})
	post(t, srv, "/api/update", events.Update{ID: "exec-abort", Stack: "a", Status: events.StatusFailed, Detail: "boom"})
	post(t, srv, "/api/finalize", events.Finalize{ID: "exec-abort", Failed: true})

	g, err := store.LoadGraph(db, "exec-abort")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]events.Status{}
	for _, s := range g.Stacks {
		got[s.Path] = s.Status
	}
	// Stack "a" failed on its own — must stay failed.
	if got["a"] != events.StatusFailed {
		t.Errorf("stack a = %q, want failed", got["a"])
	}
	// Stack "b" was still running when the run failed — must be aborted, not failed.
	if got["b"] != events.StatusAborted {
		t.Errorf("stack b = %q, want aborted (never finished, did not itself fail)", got["b"])
	}
	// Execution status must be persisted as "failure" independent of per-stack counts.
	e, err := store.GetExecution(db, "exec-abort")
	if err != nil {
		t.Fatal(err)
	}
	if e.Status != "failure" {
		t.Errorf("execution status = %q, want failure", e.Status)
	}
}

// TestClaimsListWireShape verifies that /api/claims/list encodes claims using
// snake_case json tags (events.Claim), so the runner client's []events.Claim
// decode yields non-zero fields. This is the regression test for the
// store.Claim (no tags → PascalCase) vs events.Claim (snake_case) mismatch.
func TestClaimsListWireShape(t *testing.T) {
	db := newServerTestDB(t)
	a := New(db, &MockGitHub{}, Config{})
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	// Seed a claim for env "prod", PR 42.
	expires := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	if err := store.ReplaceClaims(db, "prod", map[string]store.Claim{
		"stacks/core": {OwnerPR: 42, ExpiresAt: expires},
	}); err != nil {
		t.Fatal(err)
	}

	// POST /api/claims/list and capture the raw body.
	body, _ := json.Marshal(map[string]string{"environment": "prod"})
	resp, err := http.Post(srv.URL+"/api/claims/list", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	raw, _ := io.ReadAll(resp.Body)

	// Decode into []events.Claim — the type the runner client uses.
	var claims []events.Claim
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatalf("decode into []events.Claim: %v (body: %s)", err, raw)
	}
	if len(claims) != 1 {
		t.Fatalf("got %d claims, want 1 (body: %s)", len(claims), raw)
	}
	c := claims[0]
	if c.Environment == "" || c.StackPath == "" || c.OwnerPR == 0 {
		t.Errorf("decoded claim has zero fields — wire uses PascalCase, not snake_case: %+v (body: %s)", c, raw)
	}
	if c.Environment != "prod" || c.StackPath != "stacks/core" || c.OwnerPR != 42 {
		t.Errorf("claim fields = %+v, want prod/stacks/core/42", c)
	}
}

func TestUpdateRejectsUnknownStatus(t *testing.T) {
	db := newServerTestDB(t)
	a := New(db, &MockGitHub{}, Config{})
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	body := []byte(`{"id":"e1","stack":"a","status":"definitely-not-a-status"}`)
	resp, err := http.Post(srv.URL+"/api/update", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestFinalizeFailedMarksInitStatusesAborted(t *testing.T) {
	// Stacks stuck in "initializing" or "initialized" at a failed finalize must
	// be swept to "aborted" by the finalize handler, just like pending/running.
	db := newServerTestDB(t)
	gh := &MockGitHub{CreateCheckRunFn: func(ctx context.Context, repo, sha, env, url string) (int64, error) { return 1, nil }}
	a := New(db, gh, Config{})
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	post(t, srv, "/api/init", events.Init{
		ID: "exec-init-sweep", Repo: "o/r", SHA: "sha", PR: 10, Environment: "staging",
		Stacks: []events.StackState{
			{Path: "a", Status: events.StatusInitializing},
			{Path: "b", Status: events.StatusInitialized},
			{Path: "c", Status: events.StatusPlanned}, // already terminal — must not change
			{Path: "d", Status: events.StatusMoving},  // should be swept to aborted on failure!
		},
	})
	post(t, srv, "/api/finalize", events.Finalize{ID: "exec-init-sweep", Failed: true})

	g, err := store.LoadGraph(db, "exec-init-sweep")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]events.Status{}
	for _, s := range g.Stacks {
		got[s.Path] = s.Status
	}
	if got["a"] != events.StatusAborted {
		t.Errorf("stack a (initializing) = %q, want aborted", got["a"])
	}
	if got["b"] != events.StatusAborted {
		t.Errorf("stack b (initialized) = %q, want aborted", got["b"])
	}
	if got["c"] != events.StatusPlanned {
		t.Errorf("stack c (planned) = %q, want planned (unchanged)", got["c"])
	}
	if got["d"] != events.StatusAborted {
		t.Errorf("stack d (moving) = %q, want aborted", got["d"])
	}
}

func TestClaimsReleaseHandler(t *testing.T) {
	db := newServerTestDB(t)
	a := New(db, &MockGitHub{}, Config{})
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	// Seed a claim for env "prod", PR 42.
	expires := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	if err := store.ReplaceClaims(db, "prod", map[string]store.Claim{
		"stacks/core": {OwnerPR: 42, ExpiresAt: expires},
	}); err != nil {
		t.Fatal(err)
	}

	// Verify claims are present
	got, _ := store.ListClaims(db, "prod")
	if len(got) != 1 {
		t.Fatalf("expected 1 claim, got %d", len(got))
	}

	// Release claims via POST /api/claims/release
	body, _ := json.Marshal(map[string]any{
		"environment": "prod",
		"pr":          42,
		"stack":       "",
	})
	resp, err := http.Post(srv.URL+"/api/claims/release", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	// Verify claims are gone
	got2, _ := store.ListClaims(db, "prod")
	if len(got2) != 0 {
		t.Fatalf("expected 0 claims after release, got %d", len(got2))
	}
}

func TestInitRecoversPRFromSHAWhenMissing(t *testing.T) {
	a, _, srv := newRunTriggerApp(t)

	// A serve-queued plan run already exists for PR 12 at sha "deadbeefcafe".
	if err := store.UpsertInit(a.db, events.Init{
		ID: "run-12-nonprod-plan-deadbeefcafe-a1", Repo: "o/r", SHA: "deadbeefcafe",
		PR: 12, Environment: "nonprod", Context: "plan/nonprod",
	}); err != nil {
		t.Fatal(err)
	}

	// A rerun's runner reports Init with pr=0 (lost _PR_NUMBER) and an empty gate
	// context, same env + sha.
	body, _ := json.Marshal(events.Init{
		ID: "orphan-xyz", Repo: "o/r", SHA: "deadbeefcafe", PR: 0, Environment: "nonprod", Context: "",
	})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/init", bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// The orphan Init recovered PR 12 and superseded the serve-queued execution.
	got, err := store.GetExecution(a.db, "orphan-xyz")
	if err != nil {
		t.Fatal(err)
	}
	if got.PR != 12 {
		t.Errorf("recovered PR = %d, want 12", got.PR)
	}
	old, err := store.GetExecution(a.db, "run-12-nonprod-plan-deadbeefcafe-a1")
	if err != nil {
		t.Fatal(err)
	}
	if old.SupersededBy != "orphan-xyz" {
		t.Errorf("serve-queued execution not superseded: %+v", old)
	}
}

func TestClaimsReleaseHandlerBadRequest(t *testing.T) {
	db := newServerTestDB(t)
	a := New(db, &MockGitHub{}, Config{})
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	// Send invalid JSON body
	resp, err := http.Post(srv.URL+"/api/claims/release", "application/json", bytes.NewReader([]byte("{invalid-json}")))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 on invalid JSON", resp.StatusCode)
	}
}

func TestHandleInitProjectsViaAggregate(t *testing.T) {
	db := newServerTestDB(t)
	gh := &MockGitHub{CreateCheckRunFn: func(ctx context.Context, repo, sha, env, url string) (int64, error) { return 1, nil }}
	a := New(db, gh, Config{})
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	post(t, srv, "/api/init", events.Init{
		ID: "e1", Repo: "o/r", SHA: "sha", PR: 7, Environment: "staging",
		Stacks: []events.StackState{{Path: "a"}, {Path: "b"}},
		Edges:  []events.Edge{{From: "a", To: "b"}},
	})

	// Projection populated…
	if _, err := store.GetExecution(db, "e1"); err != nil {
		t.Fatalf("execution row missing after init: %v", err)
	}
	g, err := store.LoadGraph(db, "e1")
	if err != nil || len(g.Stacks) != 2 || len(g.Edges) != 1 {
		t.Fatalf("graph not projected: %v (%d stacks, %d edges)", err, len(g.Stacks), len(g.Edges))
	}
	// …and the run stream replays a matching State (source of truth).
	st, _, err := a.execDecider.Load(a.eventStore, runStreamID("e1"))
	if err != nil || st.PR != 7 || len(st.Stacks) != 2 {
		t.Fatalf("replayed state mismatch: %v %#v", err, st)
	}
}
