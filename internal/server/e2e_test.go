package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/reconcile"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

func TestE2EPlanLifecycle(t *testing.T) {
	db := newServerTestDB(t)
	var mu sync.Mutex
	updates := 0
	planChecks := 0
	var finalConcl string
	gh := &MockGitHub{
		CreateCheckRunFn: func(ctx context.Context, repo, sha, env, url string) (int64, error) {
			// Count only the plan/<env> gate check, not the always-on apply-lock/<env>.
			if !strings.HasPrefix(env, "apply-lock") {
				mu.Lock()
				planChecks++
				mu.Unlock()
			}
			return 42, nil
		},
		UpdateCheckRunFn: func(ctx context.Context, repo string, id int64, u CheckRunUpdate) error {
			mu.Lock()
			updates++
			if u.Conclusion != "" {
				finalConcl = u.Conclusion
			}
			mu.Unlock()
			return nil
		},
	}
	a := New(db, gh, Config{PublicBaseURL: "https://srv"})
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	post(t, srv, "/api/phase", events.PhaseEvent{ID: "e1", Repo: "o/r", SHA: "sha", PR: 7, Environment: "staging", Phase: events.PhaseWarming})
	post(t, srv, "/api/init", events.Init{ID: "e1", Repo: "o/r", SHA: "sha", PR: 7, Environment: "staging",
		Stacks: []events.StackState{{Path: "a"}, {Path: "b"}}, Edges: []events.Edge{{From: "a", To: "b"}}})
	post(t, srv, "/api/update", events.Update{ID: "e1", Stack: "a", Status: events.StatusRunning})
	post(t, srv, "/api/update", events.Update{ID: "e1", Stack: "a", Status: events.StatusPlanned})
	post(t, srv, "/api/update", events.Update{ID: "e1", Stack: "b", Status: events.StatusPlanned})
	post(t, srv, "/api/finalize", events.Finalize{ID: "e1", ReportMarkdown: "# report"})

	if planChecks != 1 {
		t.Fatalf("plan check runs created = %d, want 1", planChecks)
	}
	mu.Lock()
	defer mu.Unlock()
	if finalConcl != "success" {
		t.Fatalf("final conclusion = %q, want success", finalConcl)
	}
	if updates < 2 {
		t.Fatalf("expected several check-run patches, got %d", updates)
	}

	e, err := store.GetExecution(db, "e1")
	if err != nil {
		t.Fatal(err)
	}
	if e.ReportMarkdown != "# report" || e.Rev == 0 {
		t.Fatalf("execution = %+v", e)
	}
}

// TestInboundRebuildRecoversStuckCheck exercises the full path from Tasks 1-5:
// a serve plan run goes stuck (start_failed), an inbound Cloud Build push for a
// new build of the same sha arrives (lost _PR_NUMBER) so serve supersedes +
// adopts it, then the rebuild's runner reports Init with pr=0 — PR recovery
// reattaches it and its execution supersedes the adopted one.
func TestInboundRebuildRecoversStuckCheck(t *testing.T) {
	a, _, srv := newRunTriggerApp(t)
	a.PushVerifier = func(context.Context, string) (string, error) { return "cb-push@sa", nil }
	a.cfg.BuildTriggerNames = map[string]string{"nonprod-plan": "plan"}
	ctx := context.Background()
	repo, sha := "o/r", "feedfacefeed00"

	// 1. A serve plan run starts, then serve gives up on it (watchdog fail).
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(a.shell.Handle(ctx, 44, "nonprod", repo, reconcile.RunRequested{Kind: "plan", SHA: sha, Branch: "feat/x"}))
	oldID, ok := store.LatestExecutionID(a.db, 44, "nonprod")
	if !ok {
		t.Fatal("no queued execution")
	}
	must(a.shell.Handle(ctx, 44, "nonprod", repo, reconcile.RunStartResult{
		Kind: "plan", ExecutionID: oldID, Err: "build failed before the runner reported",
	}))

	// 2. A rebuild serve didn't launch appears (no _PR_NUMBER — lost identity).
	resp := cloudBuildPush(t, srv, map[string]any{
		"id": "build-rerun-ok", "status": "WORKING",
		"substitutions": map[string]any{"TRIGGER_NAME": "nonprod-plan", "COMMIT_SHA": sha},
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("push = %d", resp.StatusCode)
	}

	// The stuck run is superseded by an adopted execution.
	old, err := store.GetExecution(a.db, oldID)
	must(err)
	if old.SupersededBy == "" {
		t.Fatalf("stuck run not superseded: %+v", old)
	}
	adoptedID := old.SupersededBy

	// 3. The rebuild's runner reports Init (pr=0 — lost _PR_NUMBER). PR recovery
	//    reattaches it, and the fresh runner execution supersedes the adopted one —
	//    the supersede chain below is the proof.
	post(t, srv, "/api/init", events.Init{ID: "runner-rerun", Repo: repo, SHA: sha, PR: 0, Environment: "nonprod", Context: ""})
	runnerExec, err := store.GetExecution(a.db, "runner-rerun")
	must(err)
	if runnerExec.PR != 44 {
		t.Fatalf("runner Init did not recover PR: %+v", runnerExec)
	}
	// The runner execution supersedes the adopted one (same env/sha/context).
	adopted, err := store.GetExecution(a.db, adoptedID)
	must(err)
	if adopted.SupersededBy != "runner-rerun" {
		t.Fatalf("adopted execution not superseded by the runner: %+v", adopted)
	}
}
