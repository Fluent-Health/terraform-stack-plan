package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/runner"
)

func TestRunTickPostsUpdate(t *testing.T) {
	var mu sync.Mutex
	var got events.Update
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		_ = json.Unmarshal(b, &got)
		mu.Unlock()
		if r.URL.Path != "/api/update" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()
	t.Setenv(runner.EnvServer, srv.URL)
	t.Setenv(runner.EnvToken, "s")
	t.Setenv(runner.EnvExecution, "e1")

	if code := runTick([]string{"--stack", "stacks/a", "--status", "running"}); code != 0 {
		t.Fatalf("run tick = %d, want 0", code)
	}
	mu.Lock()
	defer mu.Unlock()
	if got.ID != "e1" || got.Stack != "stacks/a" || got.Status != events.StatusRunning {
		t.Errorf("update = %+v", got)
	}
}

func TestRunTickStackFromEnv(t *testing.T) {
	var mu sync.Mutex
	var got events.Update
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		_ = json.Unmarshal(b, &got)
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer srv.Close()
	t.Setenv(runner.EnvServer, srv.URL)
	t.Setenv(runner.EnvExecution, "e1")
	t.Setenv(runner.EnvStack, "stacks/b")

	if code := runTick([]string{"--status", "failed", "--detail", "boom"}); code != 0 {
		t.Fatalf("run tick = %d", code)
	}
	mu.Lock()
	defer mu.Unlock()
	if got.Stack != "stacks/b" || got.Status != events.StatusFailed || got.Detail != "boom" {
		t.Errorf("update = %+v", got)
	}
}

func TestRunTickOfflineIsNoop(t *testing.T) {
	t.Setenv(runner.EnvServer, "")
	t.Setenv(runner.EnvExecution, "e1")
	if code := runTick([]string{"--stack", "stacks/a", "--status", "running"}); code != 0 {
		t.Fatalf("offline run tick = %d, want 0", code)
	}
}

func TestRunTickBestEffortOnServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", 500)
	}))
	defer srv.Close()
	t.Setenv(runner.EnvServer, srv.URL)
	t.Setenv(runner.EnvExecution, "e1")
	if code := runTick([]string{"--stack", "a", "--status", "running"}); code != 0 {
		t.Fatalf("run tick on 500 = %d, want 0 (best-effort)", code)
	}
}

func TestRunUnknownSubcommand(t *testing.T) {
	if code := runRun([]string{"bogus"}); code != 2 {
		t.Fatalf("unknown run subcommand = %d, want 2", code)
	}
	if code := runRun(nil); code != 2 {
		t.Fatalf("bare run = %d, want 2", code)
	}
}

func TestDispatchRoutesRun(t *testing.T) {
	t.Setenv(runner.EnvServer, "")
	if code := dispatch([]string{"run", "tick", "--stack", "a", "--status", "running"}); code != 0 {
		t.Fatalf("dispatch run tick = %d, want 0", code)
	}
}
