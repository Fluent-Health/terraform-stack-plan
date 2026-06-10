package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/runner"
)

func TestRunApplyRequiresDir(t *testing.T) {
	if code := runApply([]string{}); code != 2 {
		t.Errorf("run apply with no --dir = %d, want 2", code)
	}
}

func TestRunApplyFailsClosedOnUnsatisfiedGate(t *testing.T) {
	var mu sync.Mutex
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.URL.Path]++
		mu.Unlock()
		if r.URL.Path == "/api/gate/check" {
			http.Error(w, "gate not satisfied", http.StatusConflict)
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()
	t.Setenv(runner.EnvServer, srv.URL)
	t.Setenv(runner.EnvEnvironment, "staging")
	t.Setenv("TFSTACKPLAN_PR", "7")

	if code := runApply([]string{"--dir", t.TempDir()}); code != 1 {
		t.Fatalf("run apply on unsatisfied gate = %d, want 1 (fail closed)", code)
	}
	mu.Lock()
	defer mu.Unlock()
	if hits["/api/gate/check"] != 1 {
		t.Errorf("gate check hits = %d, want 1", hits["/api/gate/check"])
	}
	if hits["/api/init"] != 0 {
		t.Errorf("init hit %d times; apply must not start when the gate is unsatisfied", hits["/api/init"])
	}
}

// applyFixture copies the apply fixture into a temp git repo with a stub
// terraform on PATH, returning the dir. Skips when terramate isn't runnable.
func applyFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS("testdata/applyfixture")); err != nil {
		t.Fatal(err)
	}
	probe := exec.Command("terramate", "version")
	probe.Dir = dir
	if out, err := probe.CombinedOutput(); err != nil {
		t.Skipf("terramate not runnable: %v: %s", err, out)
	}
	for _, a := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}, {"config", "commit.gpgsign", "false"}, {"add", "-A"}, {"commit", "-qm", "init"}} {
		c := exec.Command("git", a...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", a, err, out)
		}
	}
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "terraform"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

func TestRunApplyE2EGateSatisfied(t *testing.T) {
	dir := applyFixture(t)
	var mu sync.Mutex
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.URL.Path]++
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer srv.Close()
	t.Setenv(runner.EnvServer, srv.URL)
	t.Setenv(runner.EnvEnvironment, "staging")
	t.Setenv("TFSTACKPLAN_PR", "7")

	if code := runApply([]string{"--dir", dir, "--changed=false"}); code != 0 {
		t.Fatalf("run apply (gate satisfied) = %d, want 0", code)
	}
	for _, s := range []string{"stacks/a", "stacks/b"} {
		if _, err := os.Stat(filepath.Join(dir, s, "applied")); err != nil {
			t.Errorf("apply did not run for %s: %v", s, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if hits["/api/gate/check"] != 1 {
		t.Errorf("gate check hits = %d, want 1", hits["/api/gate/check"])
	}
	if hits["/api/gate/revoke"] != 1 {
		t.Errorf("revoke hits = %d, want 1 (post-apply cleanup)", hits["/api/gate/revoke"])
	}
}

func TestRunApplyE2EGateBlocks(t *testing.T) {
	dir := applyFixture(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/gate/check" {
			http.Error(w, "no", http.StatusConflict)
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()
	t.Setenv(runner.EnvServer, srv.URL)
	t.Setenv(runner.EnvEnvironment, "staging")
	t.Setenv("TFSTACKPLAN_PR", "7")

	if code := runApply([]string{"--dir", dir, "--changed=false"}); code != 1 {
		t.Fatalf("run apply (gate blocks) = %d, want 1", code)
	}
	if _, err := os.Stat(filepath.Join(dir, "stacks/a", "applied")); err == nil {
		t.Error("apply ran despite an unsatisfied gate")
	}
}
