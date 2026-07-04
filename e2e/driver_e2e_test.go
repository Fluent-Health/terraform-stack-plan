package e2e

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/executor"
	"github.com/Fluent-Health/terraform-stack-plan/internal/runner"
	"github.com/Fluent-Health/terraform-stack-plan/internal/server"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// recordingExecutor is an offline executor.Backend double.
type recordingExecutor struct {
	mu     sync.Mutex
	starts []executor.RunRequest
}

func (r *recordingExecutor) Start(_ context.Context, req executor.RunRequest) (executor.Ref, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.starts = append(r.starts, req)
	return executor.Ref{ID: "build-" + req.ExecutionID}, nil
}
func (r *recordingExecutor) Cancel(context.Context, executor.Ref) error { return nil }
func (r *recordingExecutor) Probe(context.Context, executor.Ref) (executor.Phase, error) {
	return executor.PhaseWorking, nil
}

// TestDriverE2E drives the whole serve-as-CI-driver loop offline:
//
//	GitHub webhook (pull_request synchronize, HMAC-signed)
//	  → decider queues the run: execution row + check run appear immediately
//	  → fake executor accepts the build
//	  → the runner replays its real protocol over the HTTP API under the
//	    serve-minted execution id (init → phase → update → finalize)
//	  → the SAME check run concludes success.
func TestDriverE2E(t *testing.T) {
	const (
		whSecret  = "e2e-webhook-secret"
		apiSecret = "e2e-api-secret"
	)
	db, err := store.Open(filepath.Join(t.TempDir(), "driver.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var mu sync.Mutex
	created := map[string]int{} // check name → creations (apply-lock/<env> is a separate, legitimate context)
	total := 0
	var conclusions []string
	gh := &server.MockGitHub{
		CreateCheckRunFn: func(_ context.Context, _, _, name, _ string) (int64, error) {
			mu.Lock()
			defer mu.Unlock()
			created[name]++
			total++
			return int64(1000 + total), nil
		},
		UpdateCheckRunFn: func(_ context.Context, _ string, _ int64, upd server.CheckRunUpdate) error {
			mu.Lock()
			defer mu.Unlock()
			if upd.Conclusion != "" {
				conclusions = append(conclusions, upd.Conclusion)
			}
			return nil
		},
	}
	app := server.New(db, gh, server.Config{
		WebhookSecret:       apiSecret,
		GitHubWebhookSecret: whSecret,
		Environment:         "nonprod",
		PublicBaseURL:       "https://serve.e2e",
	})
	exec := &recordingExecutor{}
	app.Executor = exec
	srv := httptest.NewServer(app.Routes())
	defer srv.Close()

	// 1. Webhook: PR synchronize → the check + execution exist immediately.
	payload, _ := json.Marshal(map[string]any{
		"action": "synchronize",
		"pull_request": map[string]any{
			"number": 55,
			"head":   map[string]any{"sha": "e2esha1234567890", "ref": "feat/driver"},
		},
		"repository": map[string]any{"full_name": "o/r"},
	})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/github/webhook", bytes.NewReader(payload))
	req.Header.Set("X-GitHub-Event", "pull_request")
	mac := hmac.New(sha256.New, []byte(whSecret))
	mac.Write(payload)
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("webhook = %d", resp.StatusCode)
	}
	if len(exec.starts) != 1 {
		t.Fatalf("executor starts = %+v", exec.starts)
	}
	execID := exec.starts[0].ExecutionID
	mu.Lock()
	if created["plan/nonprod"] != 1 {
		mu.Unlock()
		t.Fatalf("plan checks created = %v, want the queued check before any build", created)
	}
	mu.Unlock()

	// 2. Runner replay under the serve-minted execution id (the _EXECUTION_ID →
	//    TFSTACKPLAN_EXECUTION wiring), over the real HTTP API.
	ctx := context.Background()
	cli := runner.NewClient(srv.URL, apiSecret)
	if err := cli.Init(ctx, events.Init{
		ID: execID, Repo: "o/r", SHA: "e2esha1234567890", PR: 55, Environment: "nonprod",
		Stacks: []events.StackState{{Path: "stacks/a", Status: events.StatusPending}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := cli.Phase(ctx, events.PhaseEvent{ID: execID, Phase: events.PhaseWarming}); err != nil {
		t.Fatal(err)
	}
	if err := cli.Update(ctx, events.Update{ID: execID, Stack: "stacks/a", Status: events.StatusPlanned}); err != nil {
		t.Fatal(err)
	}
	if err := cli.Finalize(ctx, events.Finalize{ID: execID, ReportMarkdown: "# plan"}); err != nil {
		t.Fatal(err)
	}

	// 3. The SAME check run (no duplicate) concluded success.
	mu.Lock()
	defer mu.Unlock()
	if created["plan/nonprod"] != 1 {
		t.Fatalf("plan checks created = %v — the runner must land on the queued check, not mint a duplicate", created)
	}
	if len(conclusions) == 0 || conclusions[len(conclusions)-1] != "success" {
		t.Fatalf("conclusions = %v, want terminal success", conclusions)
	}
	e, err := store.GetExecution(db, execID)
	if err != nil {
		t.Fatal(err)
	}
	if e.SupersededBy != "" {
		t.Fatalf("the runner-reported execution must not be superseded: %+v", e)
	}
}
