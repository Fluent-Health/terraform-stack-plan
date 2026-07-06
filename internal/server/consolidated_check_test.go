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
