package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/execution"
	"github.com/Fluent-Health/terraform-stack-plan/internal/executor"
	"github.com/Fluent-Health/terraform-stack-plan/internal/reconcile"
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

func TestQueuedPlanCheckUsesConsolidatedName(t *testing.T) {
	var mu sync.Mutex
	var names []string
	gh := &MockGitHub{CreateCheckRunFn: func(_ context.Context, _, _, name, _ string) (int64, error) {
		mu.Lock()
		names = append(names, name)
		mu.Unlock()
		return 4242, nil
	}}
	a := New(newServerTestDB(t), gh, Config{GitHubWebhookSecret: whSecret, Environment: "nonprod", PublicBaseURL: "https://serve.test"})
	a.Executor = &fakeExecutor{}
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	webhookReq(t, srv, whSecret, "pull_request", prSyncPayload(7, "sha-one")).Body.Close()

	mu.Lock()
	defer mu.Unlock()
	if len(names) == 0 || names[0] != "terraform/nonprod" {
		t.Fatalf("queued check names = %v, want [terraform/nonprod]", names)
	}
	// Stored gate identity is untouched.
	id, _ := store.LatestExecutionID(a.db, 7, "nonprod")
	if e, _ := store.GetExecution(a.db, id); e.StatusContext != "plan/nonprod" {
		t.Errorf("StatusContext = %q, want plan/nonprod (identity unchanged)", e.StatusContext)
	}
}

func TestCheckRunRerequestedMatchesConsolidatedName(t *testing.T) {
	_, fe, srv := newRunTriggerApp(t)
	webhookReq(t, srv, whSecret, "pull_request", prSyncPayload(7, "sha-one")).Body.Close()
	payload := map[string]any{
		"action": "rerequested",
		"check_run": map[string]any{
			"name":          "terraform/nonprod",
			"head_sha":      "sha-one",
			"pull_requests": []map[string]any{{"number": 7}},
		},
		"repository": map[string]any{"full_name": "o/r"},
	}
	webhookReq(t, srv, whSecret, "check_run", payload).Body.Close()
	if len(fe.starts) != 2 {
		t.Fatalf("starts = %+v, want the consolidated-name rerun to start a second build", fe.starts)
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

// TestWatchdogFailsVanishedBuild: a queued run whose build the executor no
// longer knows must terminally fail after the timeout; a working build stays.
func TestWatchdogFailsVanishedBuild(t *testing.T) {
	a, fe, srv := newRunTriggerApp(t)

	webhookReq(t, srv, whSecret, "pull_request", prSyncPayload(7, "sha-one")).Body.Close()
	if len(fe.starts) != 1 {
		t.Fatalf("starts = %+v", fe.starts)
	}
	id := fe.starts[0].ExecutionID

	// Age the row past the watchdog timeout.
	if _, err := a.db.Exec(`UPDATE executions SET created_at = datetime('now', '-1 hour') WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}

	// Still working: the watchdog leaves it alone.
	fe.phase = executor.PhaseWorking
	a.watchRunsOnce(context.Background(), 10*time.Minute)
	e, _ := store.GetExecution(a.db, id)
	if e.Status != "in_progress" {
		t.Fatalf("working build must be left alone, status = %q", e.Status)
	}

	// Vanished: terminal failure.
	fe.phase = executor.PhaseNotFound
	a.watchRunsOnce(context.Background(), 10*time.Minute)
	e, _ = store.GetExecution(a.db, id)
	if e.Status != "failure" {
		t.Fatalf("vanished build must fail the run, status = %q", e.Status)
	}
}

// TestWatchdogIgnoresRunnerCreatedExecutions: executions the runner registered
// itself (no serve run in the stream) are not the watchdog's business.
func TestWatchdogIgnoresRunnerCreatedExecutions(t *testing.T) {
	a, _, _ := newRunTriggerApp(t)
	seedInit(t, a.shell, events.Init{ID: "runner-e1", PR: 9, Environment: "nonprod", Repo: "o/r"})
	if _, err := a.db.Exec(`UPDATE executions SET created_at = datetime('now', '-1 hour') WHERE id = 'runner-e1'`); err != nil {
		t.Fatal(err)
	}
	a.watchRunsOnce(context.Background(), 10*time.Minute)
	e, _ := store.GetExecution(a.db, "runner-e1")
	if e.Status != "in_progress" {
		t.Fatalf("runner-created execution must be untouched, status = %q", e.Status)
	}
}

// TestQueuedApplyNotConcludedSuccess: the serve-queued apply row (zero stacks,
// no phase) must stay pending — the legacy "no stacks to apply → success"
// shortcut would green-light an apply that never ran.
func TestQueuedApplyNotConcludedSuccess(t *testing.T) {
	a, fe, srv := newRunTriggerApp(t)
	payload := map[string]any{
		"ref": "refs/heads/main", "after": "mergesha123456",
		"head_commit": map[string]any{"message": "feat: y (#41)"},
		"repository":  map[string]any{"full_name": "o/r"},
	}
	webhookReq(t, srv, whSecret, "push", payload).Body.Close()
	if len(fe.starts) != 1 {
		t.Fatalf("starts = %+v", fe.starts)
	}
	e, err := store.GetExecution(a.db, fe.starts[0].ExecutionID)
	if err != nil {
		t.Fatal(err)
	}
	if e.Status != "in_progress" {
		t.Fatalf("queued apply status = %q, want in_progress (NOT success)", e.Status)
	}
}

// TestPlanStartFailureConcludesCheckFailure: a plan run that never starts must
// conclude its check run "failure" — not hang in_progress (the gate-derived
// conclusion can never say failure for a runner-less execution).
func TestPlanStartFailureConcludesCheckFailure(t *testing.T) {
	var mu sync.Mutex
	var conclusions []string
	gh := &MockGitHub{
		CreateCheckRunFn: func(context.Context, string, string, string, string) (int64, error) { return 4242, nil },
		UpdateCheckRunFn: func(_ context.Context, _ string, _ int64, upd CheckRunUpdate) error {
			mu.Lock()
			defer mu.Unlock()
			if upd.Conclusion != "" {
				conclusions = append(conclusions, upd.Conclusion)
			}
			return nil
		},
	}
	a := New(newServerTestDB(t), gh, Config{GitHubWebhookSecret: whSecret, Environment: "nonprod", PublicBaseURL: "https://serve.test"})
	fe := &fakeExecutor{startErr: context.DeadlineExceeded}
	a.Executor = fe
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	webhookReq(t, srv, whSecret, "pull_request", prSyncPayload(7, "sha-one")).Body.Close()

	mu.Lock()
	defer mu.Unlock()
	if len(conclusions) == 0 || conclusions[len(conclusions)-1] != "failure" {
		t.Fatalf("check conclusions = %v, want a terminal failure", conclusions)
	}
	id, _ := store.LatestExecutionID(a.db, 7, "nonprod")
	if e, _ := store.GetExecution(a.db, id); e.Status != "failure" {
		t.Fatalf("row status = %q, want failure", e.Status)
	}
}

// TestRunnerInitSupersedesQueuedRow: partial-cutover shape — the runner reports
// under its OWN id (BUILD_ID) with the legacy empty gate context. The
// write-time context normalization must land both rows in one supersede bucket
// so the queued twin dies instead of feeding the watchdog forever.
func TestRunnerInitSupersedesQueuedRow(t *testing.T) {
	a, fe, srv := newRunTriggerApp(t)
	webhookReq(t, srv, whSecret, "pull_request", prSyncPayload(7, "sha-one")).Body.Close()
	queuedID := fe.starts[0].ExecutionID

	// Runner init over the API: same (pr, env, sha), legacy "" context, own id.
	body, _ := json.Marshal(events.Init{ID: "build-999", Repo: "o/r", SHA: "sha-one", PR: 7, Environment: "nonprod"})
	resp, err := http.Post(srv.URL+"/api/init", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("init = %d", resp.StatusCode)
	}
	old, err := store.GetExecution(a.db, queuedID)
	if err != nil {
		t.Fatal(err)
	}
	if old.SupersededBy != "build-999" {
		t.Fatalf("queued row superseded_by = %q, want build-999", old.SupersededBy)
	}
}

// TestQueuedRowDoesNotShadowPlanForApplyLock: the apply-lock evaluation must
// read the last REPORTED plan's stacks, not the serve-queued empty row.
func TestQueuedRowDoesNotShadowPlanForApplyLock(t *testing.T) {
	a, _, srv := newRunTriggerApp(t)
	// A real plan with stacks…
	seedInit(t, a.shell, events.Init{
		ID: "real-plan", Repo: "o/r", SHA: "sha-zero", PR: 7, Environment: "nonprod",
		Stacks: []events.StackState{{Path: "stacks/a"}, {Path: "stacks/b"}},
	})
	// …then a queued run row lands on top.
	webhookReq(t, srv, whSecret, "pull_request", prSyncPayload(7, "sha-one")).Body.Close()

	stacks, ok := a.prChangedStacks("nonprod", 7)
	if !ok || len(stacks) != 2 {
		t.Fatalf("prChangedStacks = %v, %v — must read the reported plan, not the queued row", stacks, ok)
	}
}

// TestPRFromMergeSubject: the squash pattern is end-anchored so revert-style
// subjects with inner references resolve to the OUTER PR.
func TestPRFromMergeSubject(t *testing.T) {
	cases := map[string]int{
		`feat: something great (#41)`:                          41,
		`Revert "fix: allow xmove (#179)" (#190)`:              190,
		`Merge pull request #77 from Fluent-Health/feat/x`:     77,
		`hotfix straight to main`:                              0,
		`fix: mention (#12) in the middle without trailing pr`: 0,
	}
	for subject, want := range cases {
		if got := prFromMergeSubject(subject); got != want {
			t.Errorf("prFromMergeSubject(%q) = %d, want %d", subject, got, want)
		}
	}
}

// TestApplyCheckRerunRecoversPRFromStore: apply checks live on merge commits
// where GitHub sends no pull_requests — the PR comes from the execution row.
func TestApplyCheckRerunRecoversPRFromStore(t *testing.T) {
	a, fe, srv := newRunTriggerApp(t)
	// Seed the apply run via push, then complete it (runner finalize through
	// the shell) so the rerun isn't dropped as a live-apply protection.
	payload := map[string]any{
		"ref": "refs/heads/main", "after": "mergesha123456",
		"head_commit": map[string]any{"message": "feat: y (#41)"},
		"repository":  map[string]any{"full_name": "o/r"},
	}
	webhookReq(t, srv, whSecret, "push", payload).Body.Close()
	if err := a.shell.Handle(context.Background(), 41, "nonprod", "o/r",
		reconcile.RunnerFinalize{Failed: true, ApplyContext: true}); err != nil {
		t.Fatal(err)
	}

	rerun := map[string]any{
		"action": "rerequested",
		"check_run": map[string]any{
			"name":          "apply/nonprod",
			"head_sha":      "mergesha123456",
			"pull_requests": []map[string]any{},
		},
		"repository": map[string]any{"full_name": "o/r"},
	}
	webhookReq(t, srv, whSecret, "check_run", rerun).Body.Close()
	if len(fe.starts) != 2 {
		t.Fatalf("starts = %+v — apply rerun must start a second build", fe.starts)
	}
	if fe.starts[1].Kind != "apply" || fe.starts[1].PR != 41 {
		t.Fatalf("rerun request = %+v", fe.starts[1])
	}
}

// TestRunnerRecoveryAfterFalseStartFailure: a client-side start timeout whose
// build actually ran — the runner's finalize completes the run, and later
// signals must NOT flip the row back to failure (projection is batch-scoped).
func TestRunnerRecoveryAfterFalseStartFailure(t *testing.T) {
	a, fe, srv := newRunTriggerApp(t)
	fe.startErr = context.DeadlineExceeded
	webhookReq(t, srv, whSecret, "pull_request", prSyncPayload(7, "sha-one")).Body.Close()
	id := fe.starts // no start recorded on error
	_ = id
	execID, _ := store.LatestExecutionID(a.db, 7, "nonprod")
	if e, _ := store.GetExecution(a.db, execID); e.Status != "failure" {
		t.Fatalf("precondition: start-failed row = %q", e.Status)
	}

	// The build ran anyway: the runner's own Init report revives the row through
	// the aggregate alone (Started's fold clears status/superseded_by — no
	// direct store.ReviveExecution call, deleted in A3 task 3).
	if err := a.shell.HandleExec(context.Background(), execID, execution.ReportInit{Exec: execution.State{
		ID: execID, PR: 7, Environment: "nonprod", Repo: "o/r", Status: "in_progress",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := a.shell.Handle(context.Background(), 7, "nonprod", "o/r", reconcile.RunnerFinalize{}); err != nil {
		t.Fatal(err)
	}
	// A later unrelated signal (a gate tick) must not re-apply the stale failure.
	if err := a.shell.Handle(context.Background(), 7, "nonprod", "o/r", reconcile.GateTick{}); err != nil {
		t.Fatal(err)
	}
	if e, _ := store.GetExecution(a.db, execID); e.Status == "failure" {
		t.Fatalf("stale start-failure re-applied after runner recovery: %q", e.Status)
	}
}

func TestCheckSuiteRerequestedRerunsOnlyFailedRuns(t *testing.T) {
	a, fe, srv := newRunTriggerApp(t)
	webhookReq(t, srv, whSecret, "pull_request", prSyncPayload(7, "sha-one")).Body.Close()
	if len(fe.starts) != 1 {
		t.Fatalf("setup: starts = %+v", fe.starts)
	}
	id, _ := store.LatestExecutionID(a.db, 7, "nonprod")

	suite := map[string]any{
		"action":      "rerequested",
		"check_suite": map[string]any{"head_sha": "sha-one"},
		"repository":  map[string]any{"full_name": "o/r"},
	}

	// A suite re-request while the run is NOT failed re-runs nothing —
	// "Re-run failed checks" only touches failures.
	webhookReq(t, srv, whSecret, "check_suite", suite).Body.Close()
	if len(fe.starts) != 1 {
		t.Fatalf("non-failed run must not re-run: starts = %+v", fe.starts)
	}

	// Fail the run; the same suite re-request now starts a fresh attempt.
	seedTerminalStatus(t, a.shell, id, "failure")
	webhookReq(t, srv, whSecret, "check_suite", suite).Body.Close()
	if len(fe.starts) != 2 {
		t.Fatalf("failed run should re-run on suite re-request: starts = %+v", fe.starts)
	}
}
