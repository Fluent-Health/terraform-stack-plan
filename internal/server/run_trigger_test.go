package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/executor"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// fakeExecutor records Start/Cancel calls; Probe answers a fixed phase.
type fakeExecutor struct {
	mu       sync.Mutex
	starts   []executor.RunRequest
	cancels  []executor.Ref
	startErr error
	phase    executor.Phase
}

func (f *fakeExecutor) Start(_ context.Context, req executor.RunRequest) (executor.Ref, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return executor.Ref{}, f.startErr
	}
	f.starts = append(f.starts, req)
	return executor.Ref{ID: "build-" + req.ExecutionID}, nil
}

func (f *fakeExecutor) Cancel(_ context.Context, ref executor.Ref) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancels = append(f.cancels, ref)
	return nil
}

func (f *fakeExecutor) Probe(context.Context, executor.Ref) (executor.Phase, error) {
	if f.phase == "" {
		return executor.PhaseWorking, nil
	}
	return f.phase, nil
}

const whSecret = "webhook-secret"

func newRunTriggerApp(t *testing.T) (*App, *fakeExecutor, *httptest.Server) {
	t.Helper()
	gh := &MockGitHub{CreateCheckRunFn: func(context.Context, string, string, string, string) (int64, error) {
		return 4242, nil
	}}
	a := New(newServerTestDB(t), gh, Config{
		GitHubWebhookSecret: whSecret,
		Environment:         "nonprod",
		PublicBaseURL:       "https://serve.test",
	})
	fe := &fakeExecutor{}
	a.Executor = fe
	srv := httptest.NewServer(a.Routes())
	t.Cleanup(srv.Close)
	return a, fe, srv
}

func prSyncPayload(pr int, sha string) map[string]any {
	return map[string]any{
		"action": "synchronize",
		"pull_request": map[string]any{
			"number": pr,
			"head":   map[string]any{"sha": sha, "ref": "feat/x"},
		},
		"repository": map[string]any{"full_name": "o/r"},
	}
}

func TestWebhookPRSyncTriggersPlanRun(t *testing.T) {
	a, fe, srv := newRunTriggerApp(t)

	resp := webhookReq(t, srv, whSecret, "pull_request", prSyncPayload(7, "abcdef1234567890"))
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("webhook = %d", resp.StatusCode)
	}

	if len(fe.starts) != 1 {
		t.Fatalf("starts = %+v, want 1", fe.starts)
	}
	req := fe.starts[0]
	if req.Kind != "plan" || req.SHA != "abcdef1234567890" || req.Branch != "feat/x" || req.PR != 7 || req.Environment != "nonprod" {
		t.Errorf("start request = %+v", req)
	}
	// The queued execution + check run exist under the deterministic id.
	e, err := store.GetExecution(a.db, req.ExecutionID)
	if err != nil {
		t.Fatalf("queued execution missing: %v", err)
	}
	if e.PR != 7 || e.Environment != "nonprod" || e.SHA != "abcdef1234567890" || e.StatusContext != "plan/nonprod" {
		t.Errorf("execution = %+v", e)
	}
	if !e.CheckRunID.Valid || e.CheckRunID.Int64 == 0 {
		t.Error("queued execution has no check run")
	}

	// Redelivery no-ops: no second start, no new execution attempt.
	resp2 := webhookReq(t, srv, whSecret, "pull_request", prSyncPayload(7, "abcdef1234567890"))
	resp2.Body.Close()
	if len(fe.starts) != 1 {
		t.Fatalf("redelivery started another run: %+v", fe.starts)
	}
}

func TestWebhookNewSHASupersedesPlanRun(t *testing.T) {
	a, fe, srv := newRunTriggerApp(t)

	webhookReq(t, srv, whSecret, "pull_request", prSyncPayload(7, "sha-one")).Body.Close()
	webhookReq(t, srv, whSecret, "pull_request", prSyncPayload(7, "sha-two")).Body.Close()

	if len(fe.starts) != 2 {
		t.Fatalf("starts = %+v, want 2", fe.starts)
	}
	if len(fe.cancels) != 1 || fe.cancels[0].ID != "build-"+fe.starts[0].ExecutionID {
		t.Fatalf("cancels = %+v, want the first build cancelled", fe.cancels)
	}
	old, err := store.GetExecution(a.db, fe.starts[0].ExecutionID)
	if err != nil {
		t.Fatal(err)
	}
	if old.SupersededBy != fe.starts[1].ExecutionID {
		t.Errorf("old execution superseded_by = %q, want %q", old.SupersededBy, fe.starts[1].ExecutionID)
	}
}

func TestWebhookPushTriggersApplyRun(t *testing.T) {
	_, fe, srv := newRunTriggerApp(t)

	payload := map[string]any{
		"ref":   "refs/heads/main",
		"after": "mergesha123456",
		"head_commit": map[string]any{
			"message": "feat: something great (#41)\n\nbody text",
		},
		"repository": map[string]any{"full_name": "o/r"},
	}
	resp := webhookReq(t, srv, whSecret, "push", payload)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("webhook = %d", resp.StatusCode)
	}
	if len(fe.starts) != 1 {
		t.Fatalf("starts = %+v, want 1", fe.starts)
	}
	req := fe.starts[0]
	if req.Kind != "apply" || req.SHA != "mergesha123456" || req.PR != 41 {
		t.Errorf("start request = %+v", req)
	}

	// A direct push with no PR in the subject is skipped.
	payload["head_commit"] = map[string]any{"message": "hotfix straight to main"}
	payload["after"] = "direct999"
	webhookReq(t, srv, whSecret, "push", payload).Body.Close()
	if len(fe.starts) != 1 {
		t.Fatalf("direct push must not trigger, got %+v", fe.starts)
	}
}

func TestWebhookCheckRunRerequested(t *testing.T) {
	_, fe, srv := newRunTriggerApp(t)

	// Seed a run, then re-request it via the check_run button.
	webhookReq(t, srv, whSecret, "pull_request", prSyncPayload(7, "sha-one")).Body.Close()
	payload := map[string]any{
		"action": "rerequested",
		"check_run": map[string]any{
			"name":          "plan/nonprod",
			"head_sha":      "sha-one",
			"pull_requests": []map[string]any{{"number": 7}},
		},
		"repository": map[string]any{"full_name": "o/r"},
	}
	webhookReq(t, srv, whSecret, "check_run", payload).Body.Close()

	if len(fe.starts) != 2 {
		t.Fatalf("starts = %+v, want rerun to start a second build", fe.starts)
	}
	if fe.starts[1].ExecutionID == fe.starts[0].ExecutionID {
		t.Error("rerun must mint a fresh execution id")
	}
	if len(fe.cancels) != 1 {
		t.Errorf("rerun of a live run should cancel it, cancels = %+v", fe.cancels)
	}

	// A foreign tier's check name is ignored.
	payload["check_run"].(map[string]any)["name"] = "plan/prod"
	webhookReq(t, srv, whSecret, "check_run", payload).Body.Close()
	if len(fe.starts) != 2 {
		t.Fatalf("foreign-tier check rerequest must be ignored, got %+v", fe.starts)
	}
}

func TestWebhookRunTriggerDisarmedWithoutExecutor(t *testing.T) {
	a := New(newServerTestDB(t), &MockGitHub{}, Config{
		GitHubWebhookSecret: whSecret,
		Environment:         "nonprod",
	})
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	resp := webhookReq(t, srv, whSecret, "pull_request", prSyncPayload(7, "sha-one"))
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("webhook = %d", resp.StatusCode)
	}
	if _, ok := store.LatestExecutionID(a.db, 7, "nonprod"); ok {
		t.Fatal("disarmed serve must not create executions")
	}
}

func TestWebhookStartFailureFailsCheck(t *testing.T) {
	a, fe, srv := newRunTriggerApp(t)
	fe.startErr = context.DeadlineExceeded

	webhookReq(t, srv, whSecret, "pull_request", prSyncPayload(7, "sha-one")).Body.Close()

	id, ok := store.LatestExecutionID(a.db, 7, "nonprod")
	if !ok {
		t.Fatal("queued execution should exist even when the start fails")
	}
	e, err := store.GetExecution(a.db, id)
	if err != nil {
		t.Fatal(err)
	}
	if e.Status != "failure" {
		t.Errorf("execution status = %q, want failure (start never happened)", e.Status)
	}
}
