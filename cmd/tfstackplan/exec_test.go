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

func TestRunExecRequiresCommand(t *testing.T) {
	if code := runExec([]string{}); code != 2 {
		t.Errorf("no args: got %d, want 2", code)
	}
	if code := runExec([]string{"--"}); code != 2 {
		t.Errorf("bare --: got %d, want 2", code)
	}
}

func TestRunExecCustomPhase(t *testing.T) {
	if code := runExec([]string{"--phase", "custom", "--", "echo", "hi"}); code != 0 {
		t.Errorf("custom phase: got %d, want 0", code)
	}
}

func TestRunExecSuccess(t *testing.T) {
	var mu sync.Mutex
	var gotPhase events.PhaseEvent
	var finalized bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/api/phase":
			_ = json.Unmarshal(b, &gotPhase)
		case "/api/finalize":
			finalized = true
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	t.Setenv(runner.EnvServer, srv.URL)
	t.Setenv(runner.EnvExecution, "build-1")
	t.Setenv(runner.EnvEnvironment, "nonprod")
	t.Setenv("TFSTACKPLAN_REPO", "o/r")
	t.Setenv("TFSTACKPLAN_SHA", "abc")
	t.Setenv("TFSTACKPLAN_PR", "5")

	if code := runExec([]string{"--phase", "linting", "--", "echo", "ok"}); code != 0 {
		t.Fatalf("success run: got %d, want 0", code)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotPhase.Phase != events.PhaseLinting {
		t.Errorf("phase = %s, want %s", gotPhase.Phase, events.PhaseLinting)
	}
	if gotPhase.ID != "build-1" || gotPhase.Repo != "o/r" || gotPhase.SHA != "abc" ||
		gotPhase.PR != 5 || gotPhase.Environment != "nonprod" {
		t.Errorf("phase event fields = %+v", gotPhase)
	}
	if finalized {
		t.Error("Finalize must NOT be called on success")
	}
}

func TestRunExecFailure(t *testing.T) {
	var mu sync.Mutex
	var gotFinal events.Finalize

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/api/finalize" {
			_ = json.Unmarshal(b, &gotFinal)
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	t.Setenv(runner.EnvServer, srv.URL)
	t.Setenv(runner.EnvExecution, "build-2")

	code := runExec([]string{"--phase", "linting", "--", "false"})
	if code == 0 {
		t.Fatal("failing command: got exit 0, want non-zero")
	}

	mu.Lock()
	defer mu.Unlock()
	if !gotFinal.Failed {
		t.Error("expected Finalize with Failed=true on command failure")
	}
	if gotFinal.ID != "build-2" {
		t.Errorf("finalize ID = %q, want %q", gotFinal.ID, "build-2")
	}
}

func TestRunExecOfflineCommandStillRuns(t *testing.T) {
	t.Setenv(runner.EnvServer, "")
	t.Setenv(runner.EnvExecution, "build-3")
	if code := runExec([]string{"--phase", "linting", "--", "echo", "ok"}); code != 0 {
		t.Fatalf("offline: got %d, want 0", code)
	}
}

func TestRunExecNoExecIDSkipsServer(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	}))
	defer srv.Close()

	t.Setenv(runner.EnvServer, srv.URL)
	t.Setenv(runner.EnvExecution, "")

	if code := runExec([]string{"--phase", "linting", "--", "echo", "ok"}); code != 0 {
		t.Fatalf("no exec id: got %d, want 0", code)
	}
	if called {
		t.Error("server must not be called when TFSTACKPLAN_EXECUTION is empty")
	}
}

func TestRunExecNoPhaseSkipsPhasePost(t *testing.T) {
	phaseCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/phase" {
			phaseCalled = true
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	t.Setenv(runner.EnvServer, srv.URL)
	t.Setenv(runner.EnvExecution, "build-4")

	if code := runExec([]string{"--", "echo", "ok"}); code != 0 {
		t.Fatalf("no --phase: got %d, want 0", code)
	}
	if phaseCalled {
		t.Error("phase must not be posted when --phase is omitted")
	}
}

func TestDispatchRoutesRunExec(t *testing.T) {
	t.Setenv(runner.EnvServer, "")
	t.Setenv(runner.EnvExecution, "")
	if code := dispatch([]string{"run", "exec", "--", "echo", "hi"}); code != 0 {
		t.Fatalf("dispatch run exec = %d, want 0", code)
	}
}
