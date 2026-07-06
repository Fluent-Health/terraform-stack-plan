package server

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/claims"
	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/reconcile"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// consolidatedApp: armed serve + a MockGitHub that records every check update.
func consolidatedApp(t *testing.T) (*App, *fakeExecutor, *httptest.Server, func() []CheckRunUpdate) {
	t.Helper()
	var mu sync.Mutex
	var updates []CheckRunUpdate
	gh := &MockGitHub{
		CreateCheckRunFn: func(context.Context, string, string, string, string) (int64, error) { return 4242, nil },
		UpdateCheckRunFn: func(_ context.Context, _ string, _ int64, upd CheckRunUpdate) error {
			mu.Lock()
			updates = append(updates, upd)
			mu.Unlock()
			return nil
		},
	}
	a := New(newServerTestDB(t), gh, Config{GitHubWebhookSecret: whSecret, Environment: "nonprod", PublicBaseURL: "https://serve.test"})
	fe := &fakeExecutor{}
	a.Executor = fe
	srv := httptest.NewServer(a.Routes())
	t.Cleanup(srv.Close)
	snap := func() []CheckRunUpdate {
		mu.Lock()
		defer mu.Unlock()
		return append([]CheckRunUpdate(nil), updates...)
	}
	return a, fe, srv, snap
}

// TestConsolidatedCheckHoldsOnOverlappingApply: a clean plan whose stacks
// overlap another PR's in-flight apply must stay in_progress with the
// "waiting on PR #N's apply" title, and record a held pr_head entry pointing
// at the execution.
func TestConsolidatedCheckHoldsOnOverlappingApply(t *testing.T) {
	a, fe, srv, snap := consolidatedApp(t)

	// PR #3 is applying stacks/a right now.
	if err := a.shell.handleClaim("nonprod", claims.AcquireClaim{PR: 3, Stacks: []string{"stacks/a"}, Now: a.now()}); err != nil {
		t.Fatal(err)
	}

	// PR #7 opens; the queued run is created…
	webhookReq(t, srv, whSecret, "pull_request", prSyncPayload(7, "sha-one")).Body.Close()
	id := fe.starts[0].ExecutionID

	// …the runner replays the same execution id and finalizes a clean plan
	// touching stacks/a.
	if err := store.UpsertInit(a.db, events.Init{
		ID: id, Repo: "o/r", SHA: "sha-one", PR: 7, Environment: "nonprod",
		Context: "plan/nonprod", Stacks: []events.StackState{{Path: "stacks/a", Status: events.StatusSafe}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetReport(a.db, id, "report"); err != nil {
		t.Fatal(err)
	}
	if err := a.shell.Handle(context.Background(), 7, "nonprod", "o/r", reconcile.RunnerFinalize{}); err != nil {
		t.Fatal(err)
	}

	upds := snap()
	if len(upds) == 0 {
		t.Fatal("no check updates recorded")
	}
	last := upds[len(upds)-1]
	if last.Conclusion != "" {
		t.Fatalf("conclusion = %q, want in_progress (lock held)", last.Conclusion)
	}
	if last.Title != "waiting on PR #3's apply" {
		t.Errorf("title = %q", last.Title)
	}
	rec, ok, err := store.GetApplyLockCheck(a.db, "nonprod", "sha-one")
	if err != nil || !ok {
		t.Fatalf("no applylock record: %v ok=%v", err, ok)
	}
	if rec.State != "held" || rec.Kind != "pr_head" || rec.ExecutionID != id || rec.CheckRunID != 4242 {
		t.Errorf("record = %+v", rec)
	}
}

// TestConsolidatedCheckClearConcludesSuccess: no overlap → success, no lock
// section, and the record persisted as clear.
func TestConsolidatedCheckClearConcludesSuccess(t *testing.T) {
	a, fe, srv, snap := consolidatedApp(t)
	webhookReq(t, srv, whSecret, "pull_request", prSyncPayload(7, "sha-one")).Body.Close()
	id := fe.starts[0].ExecutionID
	if err := store.UpsertInit(a.db, events.Init{
		ID: id, Repo: "o/r", SHA: "sha-one", PR: 7, Environment: "nonprod",
		Context: "plan/nonprod", Stacks: []events.StackState{{Path: "stacks/a", Status: events.StatusSafe}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetReport(a.db, id, "report"); err != nil {
		t.Fatal(err)
	}
	if err := a.shell.Handle(context.Background(), 7, "nonprod", "o/r", reconcile.RunnerFinalize{}); err != nil {
		t.Fatal(err)
	}
	upds := snap()
	last := upds[len(upds)-1]
	if last.Conclusion != "success" {
		t.Fatalf("conclusion = %q, want success", last.Conclusion)
	}
	if strings.Contains(last.Text, "Merge lock") {
		t.Errorf("clear verdict must render no lock section, text = %q", last.Text)
	}
	rec, ok, _ := store.GetApplyLockCheck(a.db, "nonprod", "sha-one")
	if !ok || rec.State != "clear" {
		t.Errorf("record = %+v ok=%v, want clear", rec, ok)
	}
}

// TestConsolidatedReleaseFlipsHeldToSuccess: when the overlapping apply
// releases its claim, the held consolidated check must be re-driven to a
// success conclusion (not a lock-only apply-lock patch). The finalize goes
// through POST /api/finalize so the handler-level postPlanApplyLock gate is
// exercised: ungated, it would re-patch the consolidated check with lock-only
// content ("apply-lock: holding merge") right after the terminal render.
func TestConsolidatedReleaseFlipsHeldToSuccess(t *testing.T) {
	a, fe, srv, snap := consolidatedApp(t)
	if err := a.shell.handleClaim("nonprod", claims.AcquireClaim{PR: 3, Stacks: []string{"stacks/a"}, Now: a.now()}); err != nil {
		t.Fatal(err)
	}
	webhookReq(t, srv, whSecret, "pull_request", prSyncPayload(7, "sha-one")).Body.Close()
	id := fe.starts[0].ExecutionID
	if err := store.UpsertInit(a.db, events.Init{
		ID: id, Repo: "o/r", SHA: "sha-one", PR: 7, Environment: "nonprod",
		Context: "plan/nonprod", Stacks: []events.StackState{{Path: "stacks/a", Status: events.StatusSafe}},
	}); err != nil {
		t.Fatal(err)
	}
	if code := post(t, srv, "/api/finalize", events.Finalize{ID: id, ReportMarkdown: "report"}); code != 200 {
		t.Fatalf("finalize = %d", code)
	}
	// Precondition: the LAST update after finalize is the held consolidated
	// render — not a lock-only postPlanApplyLock re-patch of the same check.
	if last := snap()[len(snap())-1]; last.Conclusion != "" || last.Title != "waiting on PR #3's apply" {
		t.Fatalf("precondition: last update after finalize = title %q conclusion %q, want held render", last.Title, last.Conclusion)
	}

	// PR #3's apply finishes.
	a.releaseApplyClaims(context.Background(), "nonprod", 3)

	upds := snap()
	last := upds[len(upds)-1]
	if last.Conclusion != "success" {
		t.Fatalf("post-release conclusion = %q, want success", last.Conclusion)
	}
	// The release must re-drive the FULL consolidated render (a.drive), not a
	// lock-only patch: the legacy reevaluateHeld→postApplyLock path titles the
	// check "apply-lock: …" and carries no Text/report at all.
	if strings.HasPrefix(last.Title, "apply-lock:") {
		t.Errorf("post-release title = %q — lock-only patch, want full render", last.Title)
	}
	if !strings.Contains(last.Text, "report") {
		t.Errorf("post-release text %q lacks the report markdown — lock-only patch, want full render", last.Text)
	}
	rec, ok, _ := store.GetApplyLockCheck(a.db, "nonprod", "sha-one")
	if !ok || rec.State != "clear" {
		t.Errorf("record after release = %+v ok=%v, want clear", rec, ok)
	}
}

// TestArmedTierPostsNoSeparateApplyLockCheck: the pull_request webhook must not
// create an apply-lock/<env> check run on an armed tier. A REPORTED execution is
// seeded first so handlePRApplyLock's per-env loop actually runs (EnvironmentsForPR
// non-empty, prChangedStacks ok): ungated, it would create a second check run for
// the lock on the PR head, on top of the queued run's terraform/<env> check.
func TestArmedTierPostsNoSeparateApplyLockCheck(t *testing.T) {
	var mu sync.Mutex
	var created []string
	gh := &MockGitHub{
		CreateCheckRunFn: func(_ context.Context, _, _, name, _ string) (int64, error) {
			mu.Lock()
			created = append(created, name)
			mu.Unlock()
			return 4242, nil
		},
		PRHeadSHAFn: func(context.Context, string, int) (string, error) { return "sha-one", nil },
	}
	a := New(newServerTestDB(t), gh, Config{GitHubWebhookSecret: whSecret, Environment: "nonprod", PublicBaseURL: "https://serve.test"})
	a.Executor = &fakeExecutor{}
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	// Seed a reported plan for PR 7 in nonprod (stacks present ⇒
	// LatestReportedExecutionID finds it, prChangedStacks returns ok=true).
	if err := store.UpsertInit(a.db, events.Init{
		ID: "seed-1", Repo: "o/r", SHA: "sha-zero", PR: 7, Environment: "nonprod",
		Context: "plan/nonprod", Stacks: []events.StackState{{Path: "stacks/a", Status: events.StatusSafe}},
	}); err != nil {
		t.Fatal(err)
	}

	webhookReq(t, srv, whSecret, "pull_request", prSyncPayload(7, "sha-one")).Body.Close()
	mu.Lock()
	defer mu.Unlock()
	for _, n := range created {
		if strings.HasPrefix(n, "apply-lock/") {
			t.Fatalf("armed tier created %q — apply-lock is folded into terraform/<env>", n)
		}
	}
	// Exactly one check run: the queued run's terraform/nonprod. An ungated
	// handlePRApplyLock would create a second one for the pr_head lock surface.
	if len(created) != 1 {
		t.Fatalf("created %d check runs %v, want exactly 1 (the queued run's)", len(created), created)
	}
}

// TestMergeGroupCheckNameFollowsArming: merge-group heads keep a lock-only check
// posted via postApplyLock, whose NAME must be the consolidated terraform/<env>
// on an armed tier and stay apply-lock/<env> unarmed.
func TestMergeGroupCheckNameFollowsArming(t *testing.T) {
	run := func(t *testing.T, armed bool, want string) {
		var mu sync.Mutex
		var created []string
		gh := &MockGitHub{
			CreateCheckRunFn: func(_ context.Context, _, _, name, _ string) (int64, error) {
				mu.Lock()
				created = append(created, name)
				mu.Unlock()
				return 4242, nil
			},
			MergeGroupPRsFn: func(context.Context, string, string) ([]int, error) { return []int{5}, nil },
		}
		a := New(newServerTestDB(t), gh, Config{GitHubWebhookSecret: whSecret, Environment: "nonprod", PublicBaseURL: "https://serve.test"})
		if armed {
			a.Executor = &fakeExecutor{}
		}
		// Reported plan for PR 5 so prChangedStacks resolves the group's stacks.
		if err := store.UpsertInit(a.db, events.Init{
			ID: "seed-5", Repo: "o/r", SHA: "sha-five", PR: 5, Environment: "nonprod",
			Context: "plan/nonprod", Stacks: []events.StackState{{Path: "stacks/a", Status: events.StatusSafe}},
		}); err != nil {
			t.Fatal(err)
		}
		if err := a.handleMergeGroup(context.Background(), "o/r", "mg-sha", "checks_requested"); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(created) != 1 || created[0] != want {
			t.Fatalf("merge-group check runs = %v, want exactly [%s]", created, want)
		}
	}
	t.Run("armed", func(t *testing.T) { run(t, true, "terraform/nonprod") })
	t.Run("unarmed", func(t *testing.T) { run(t, false, "apply-lock/nonprod") })
}
