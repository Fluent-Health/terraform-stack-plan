package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/reconcile"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

func cloudBuildPush(t *testing.T, srv *httptest.Server, build map[string]any) *http.Response {
	t.Helper()
	data, _ := json.Marshal(build)
	env := map[string]any{"message": map[string]any{"data": base64.StdEncoding.EncodeToString(data)}}
	body, _ := json.Marshal(env)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/pubsub/cloud-builds", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer faketoken")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestCloudBuildPushAdoptsRebuild(t *testing.T) {
	a, _, srv := newRunTriggerApp(t)
	a.PushVerifier = func(context.Context, string) (string, error) { return "cb-push@sa", nil }
	a.cfg.BuildTriggerNames = map[string]string{"nonprod-plan": "plan"}

	// A serve plan run for PR 30 that serve has given up on (start_failed) — mirror
	// state by writing the failed execution AND replaying the run events so gather
	// reconstructs Runs[plan]. Simplest: drive the run to start_failed via the shell.
	repo := "o/r"
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(a.shell.Handle(context.Background(), 30, "nonprod", repo, reconcile.RunRequested{Kind: "plan", SHA: "caf00dcafe00", Branch: "feat/x"}))
	// The fake executor started build "build-run-30-...". Force the run to
	// start_failed so the inbound path treats a new build as a rebuild.
	oldID := "run-30-nonprod-plan-cafe00dcafe0-a1" // recompute below if the short-sha differs
	if _, err := store.GetExecution(a.db, oldID); err != nil {
		// Recover the real id from the queued row (deterministic id uses sha[:12]).
		id, ok := store.LatestExecutionID(a.db, 30, "nonprod")
		if !ok {
			t.Fatal("no queued execution")
		}
		oldID = id
	}
	must(a.shell.Handle(context.Background(), 30, "nonprod", repo, reconcile.RunStartResult{
		Kind: "plan", ExecutionID: oldID, Err: "build failed before the runner reported",
	}))

	// Inbound Cloud Build event: a NEW build for the same sha from serve's plan trigger.
	resp := cloudBuildPush(t, srv, map[string]any{
		"id":     "build-rerun-999",
		"status": "SUCCESS",
		"substitutions": map[string]any{
			"TRIGGER_NAME": "nonprod-plan",
			"COMMIT_SHA":   "caf00dcafe00",
			"_PR_NUMBER":   "30",
		},
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("push = %d", resp.StatusCode)
	}

	// The old run is superseded by a new adopted execution bound to build-rerun-999.
	old, err := store.GetExecution(a.db, oldID)
	if err != nil {
		t.Fatal(err)
	}
	if old.SupersededBy == "" {
		t.Errorf("old run not superseded: %+v", old)
	}
	newID := old.SupersededBy
	if _, err := store.GetExecution(a.db, newID); err != nil {
		t.Fatalf("adopted execution %q missing: %v", newID, err)
	}
}

func TestCloudBuildPushIgnoresForeignTrigger(t *testing.T) {
	a, _, srv := newRunTriggerApp(t)
	a.PushVerifier = func(context.Context, string) (string, error) { return "cb-push@sa", nil }
	a.cfg.BuildTriggerNames = map[string]string{"nonprod-plan": "plan"}

	resp := cloudBuildPush(t, srv, map[string]any{
		"id":            "someone-elses-build",
		"status":        "SUCCESS",
		"substitutions": map[string]any{"TRIGGER_NAME": "unrelated-service-build", "COMMIT_SHA": "x"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent { // acked, but no-op
		t.Fatalf("push = %d", resp.StatusCode)
	}
}
