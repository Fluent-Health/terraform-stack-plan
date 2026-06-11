package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/runner"
	"github.com/Fluent-Health/terraform-stack-plan/internal/statemove"
)

// TestRunApplyAbortsOnPendingStateMove asserts the fail-closed pre-phase: a
// pending cross-state move manifest must be executed before the apply, and when
// it cannot run cleanly the whole apply aborts (exit 1). No TFSTACKPLAN_SERVER,
// so the gate no-ops; the move can't execute against an unprepared stack.
func TestRunApplyAbortsOnPendingStateMove(t *testing.T) {
	if _, err := exec.LookPath("terraform"); err != nil {
		t.Skip("terraform not on PATH")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "stacks/b")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	xm := statemove.XMove{SourceStack: "stacks/a", Pairs: []statemove.Move{{From: "x.y", To: "x.y"}}}
	if err := os.WriteFile(filepath.Join(dir, statemove.XMoveFileName("PR-1")), []byte(statemove.RenderXMove("PR-1", xm)), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := runApply([]string{"--dir", root, "--changed=false"}); code != 1 {
		t.Fatalf("run apply with a pending cross-state move = %d, want 1 (fail-closed pre-phase)", code)
	}
}

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

// TestRunApplyImpersonatesRequester verifies that --impersonate-requester mints
// a token for the SA returned by the gate-check and exports it as
// GOOGLE_OAUTH_ACCESS_TOKEN before the apply runs.
func TestRunApplyImpersonatesRequester(t *testing.T) {
	dir := applyFixture(t)

	// Stub mintAccessToken: record the SA it was called with, return a sentinel.
	var calledWith string
	orig := mintAccessToken
	mintAccessToken = func(_ context.Context, sa string) (string, error) {
		calledWith = sa
		return "tok-123", nil
	}
	defer func() {
		mintAccessToken = orig
		os.Unsetenv("GOOGLE_OAUTH_ACCESS_TOKEN")
	}()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/gate/check" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"requester":"poolA@x"}`))
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()
	t.Setenv(runner.EnvServer, srv.URL)
	t.Setenv(runner.EnvEnvironment, "staging")
	t.Setenv("TFSTACKPLAN_PR", "7")

	code := runApply([]string{"--dir", dir, "--changed=false", "--impersonate-requester"})
	if code != 0 {
		t.Fatalf("run apply (impersonate-requester) = %d, want 0", code)
	}
	if calledWith != "poolA@x" {
		t.Errorf("mintAccessToken called with %q, want poolA@x", calledWith)
	}
	if got := os.Getenv("GOOGLE_OAUTH_ACCESS_TOKEN"); got != "tok-123" {
		t.Errorf("GOOGLE_OAUTH_ACCESS_TOKEN = %q, want tok-123", got)
	}
}

// TestRunApplyNoImpersonateWhenFlagAbsent verifies that without
// --impersonate-requester, mintAccessToken is not called and
// GOOGLE_OAUTH_ACCESS_TOKEN is not set by runApply.
func TestRunApplyNoImpersonateWhenFlagAbsent(t *testing.T) {
	dir := applyFixture(t)
	os.Unsetenv("GOOGLE_OAUTH_ACCESS_TOKEN")

	// Stub mintAccessToken to fail the test if called.
	orig := mintAccessToken
	mintAccessToken = func(_ context.Context, sa string) (string, error) {
		t.Errorf("mintAccessToken called unexpectedly with %q", sa)
		return "", nil
	}
	defer func() { mintAccessToken = orig }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/gate/check" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"requester":"poolA@x"}`))
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()
	t.Setenv(runner.EnvServer, srv.URL)
	t.Setenv(runner.EnvEnvironment, "staging")
	t.Setenv("TFSTACKPLAN_PR", "7")

	code := runApply([]string{"--dir", dir, "--changed=false"})
	if code != 0 {
		t.Fatalf("run apply (no impersonate flag) = %d, want 0", code)
	}
	if got := os.Getenv("GOOGLE_OAUTH_ACCESS_TOKEN"); got != "" {
		t.Errorf("GOOGLE_OAUTH_ACCESS_TOKEN set to %q but should be empty", got)
	}
}
