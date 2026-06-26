package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/runner"
)

func TestDispatchRoutesRunLint(t *testing.T) {
	if code := dispatch([]string{"run", "lint", "--dir", filepath.Join(t.TempDir(), "nope")}); code == 0 {
		t.Error("run lint on a missing dir should be non-zero")
	}
}

func TestRunLintRequiresDir(t *testing.T) {
	if code := runLint([]string{}); code != 2 {
		t.Errorf("expected exit code 2 when --dir is missing, got %d", code)
	}
}

func TestRunLintSuccess(t *testing.T) {
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS("testdata/planfixture")); err != nil {
		t.Fatal(err)
	}
	probe := exec.Command("terramate", "version")
	if out, err := probe.CombinedOutput(); err != nil {
		t.Skipf("terramate not runnable: %v: %s", err, out)
	}

	// Append a mock "lint" script block so Terramate has a valid "lint" command to run.
	mockScript := `
script "lint" {
  job {
    commands = [
      ["echo", "mock-lint"],
    ]
  }
}
`
	f, err := os.OpenFile(filepath.Join(dir, "terramate.tm.hcl"), os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(mockScript); err != nil {
		t.Fatal(err)
	}
	f.Close()

	for _, a := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}, {"config", "commit.gpgsign", "false"}, {"add", "-A"}, {"commit", "-qm", "init"}} {
		c := exec.Command("git", a...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", a, err, out)
		}
	}

	var mu sync.Mutex
	var gotInit events.Init
	var gotPhase events.PhaseEvent
	var finalized bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/api/init":
			_ = json.Unmarshal(b, &gotInit)
		case "/api/phase":
			_ = json.Unmarshal(b, &gotPhase)
		case "/api/finalize":
			finalized = true
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	t.Setenv(runner.EnvServer, srv.URL)
	t.Setenv(runner.EnvEnvironment, "staging")
	t.Setenv(runner.EnvExecution, "")

	if code := runLint([]string{"--dir", dir, "--changed=false"}); code != 0 {
		t.Fatalf("run lint = %d, want 0", code)
	}

	mu.Lock()
	defer mu.Unlock()

	if len(gotInit.Stacks) != 2 {
		t.Errorf("init stacks = %d, want 2", len(gotInit.Stacks))
	}
	if gotPhase.Phase != events.PhaseLinting {
		t.Errorf("got phase = %s, want %s", gotPhase.Phase, events.PhaseLinting)
	}
	if finalized {
		t.Error("expected successful run NOT to call finalize")
	}
}

func TestRunLintFailure(t *testing.T) {
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS("testdata/planfixture")); err != nil {
		t.Fatal(err)
	}
	probe := exec.Command("terramate", "version")
	if out, err := probe.CombinedOutput(); err != nil {
		t.Skipf("terramate not runnable: %v: %s", err, out)
	}

	// Append a mock "lint" script block.
	mockScript := `
script "lint" {
  job {
    commands = [
      ["echo", "mock-lint"],
    ]
  }
}
`
	f, err := os.OpenFile(filepath.Join(dir, "terramate.tm.hcl"), os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(mockScript); err != nil {
		t.Fatal(err)
	}
	f.Close()

	for _, a := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}, {"config", "commit.gpgsign", "false"}, {"add", "-A"}, {"commit", "-qm", "init"}} {
		c := exec.Command("git", a...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", a, err, out)
		}
	}

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
	t.Setenv(runner.EnvEnvironment, "staging")
	t.Setenv(runner.EnvExecution, "")

	if code := runLint([]string{"--dir", dir, "--changed=false", "--script=nonexistent"}); code != 1 {
		t.Fatalf("run lint = %d, want 1", code)
	}

	mu.Lock()
	defer mu.Unlock()

	if !gotFinal.Failed {
		t.Error("expected finalize with Failed=true on lint failure")
	}
}
