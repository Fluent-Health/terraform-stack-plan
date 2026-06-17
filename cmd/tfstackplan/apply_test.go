package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/runner"
	"github.com/Fluent-Health/terraform-stack-plan/internal/statemove"
)

// TestRunApplyAbortsOnPendingStateMove asserts the fail-closed pre-phase: a
// pending cross-state move manifest must be executed before the apply, and when
// it cannot run cleanly the whole apply aborts (exit 1). No TFSTACKPLAN_SERVER,
// so the gate no-ops; the move can't execute against an unprepared stack. A fake
// terramate supplies one changed stack so the run reaches the move pre-phase
// (under the self-healing ordering, stacks are computed before the gate/move).
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
	withFakeTM(t, &fakeTM{changed: []string{"stacks/b"}}, nil)
	if code := runApply([]string{"--dir", root}); code != 1 {
		t.Fatalf("run apply with a pending cross-state move = %d, want 1 (fail-closed pre-phase)", code)
	}
}

func TestRunApplyRequiresDir(t *testing.T) {
	if code := runApply([]string{}); code != 2 {
		t.Errorf("run apply with no --dir = %d, want 2", code)
	}
}

// TestRunApplyFailsClosedOnUnsatisfiedGate asserts the fail-closed gate: with a
// changed stack and a gate/check that 409s, apply checks the gate exactly once,
// never runs the apply script, and exits 1. Under the self-healing ordering the
// apply execution IS registered (Init) before the gate so the rejection renders
// in the apply check run; the apply script is the line that must not be crossed.
func TestRunApplyFailsClosedOnUnsatisfiedGate(t *testing.T) {
	srv, rs := stubServer(t)
	rs.gateCheckStatus = http.StatusConflict
	setApplyEnv(t, srv.URL, 7, "staging")
	f := &fakeTM{changed: []string{"stacks/a"}}
	withFakeTM(t, f, nil)

	if code := runApply([]string{"--dir", t.TempDir()}); code != 1 {
		t.Fatalf("run apply on unsatisfied gate = %d, want 1 (fail closed)", code)
	}
	if rs.gateCheckHits != 1 {
		t.Errorf("gate check hits = %d, want 1", rs.gateCheckHits)
	}
	if f.scriptRan {
		t.Error("apply script ran despite an unsatisfied gate")
	}
	if !rs.has("/api/init") {
		t.Error("apply execution should be registered (Init) so the gate rejection renders in the check run")
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

	// Fix I2: use t.Setenv so the prior value (or absence) of
	// GOOGLE_OAUTH_ACCESS_TOKEN is auto-restored after the test.
	t.Setenv("GOOGLE_OAUTH_ACCESS_TOKEN", "")

	// Stub mintAccessToken: record the SA it was called with, return a sentinel.
	var calledWith string
	orig := mintAccessToken
	mintAccessToken = func(_ context.Context, sa string) (string, error) {
		calledWith = sa
		return "tok-123", nil
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

// TestRunApplyMintFailClosedImpersonate verifies that when --impersonate-requester
// is set and mintAccessToken returns an error, runApply returns 1 (fail-closed)
// without running any apply.
func TestRunApplyMintFailClosedImpersonate(t *testing.T) {
	dir := applyFixture(t)

	// Fix I2: ensure env var hygiene via t.Setenv.
	t.Setenv("GOOGLE_OAUTH_ACCESS_TOKEN", "")

	orig := mintAccessToken
	mintAccessToken = func(_ context.Context, sa string) (string, error) {
		return "", fmt.Errorf("credentials unavailable")
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

	code := runApply([]string{"--dir", dir, "--changed=false", "--impersonate-requester"})
	if code != 1 {
		t.Fatalf("run apply (mint failure) = %d, want 1 (fail-closed)", code)
	}
	// No apply must have run.
	if _, err := os.Stat(filepath.Join(dir, "stacks/a", "applied")); err == nil {
		t.Error("apply ran despite mint failure — should have been fail-closed")
	}
}

// TestRunApplyNoImpersonateWhenFlagAbsent verifies that without
// --impersonate-requester, mintAccessToken is not called and
// GOOGLE_OAUTH_ACCESS_TOKEN is not set by runApply.
func TestRunApplyNoImpersonateWhenFlagAbsent(t *testing.T) {
	dir := applyFixture(t)

	// Fix I2: use t.Setenv so the prior value (or absence) of
	// GOOGLE_OAUTH_ACCESS_TOKEN is auto-restored after the test.
	t.Setenv("GOOGLE_OAUTH_ACCESS_TOKEN", "")

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

// TestApplyZeroStacksSkipsGate asserts a zero-changed-stack apply short-circuits
// to success WITHOUT touching the gate: it Inits the (empty) apply execution and
// finalizes, never calling /api/gate/check. Reproduces #332 (bootstrap-only PR).
func TestApplyZeroStacksSkipsGate(t *testing.T) {
	srv, rs := stubServer(t)
	setApplyEnv(t, srv.URL, 332, "prod")
	withFakeTM(t, &fakeTM{changed: nil}, nil)

	code := runApply([]string{"--dir", t.TempDir()})
	if code != 0 {
		t.Fatalf("exit=%d want 0", code)
	}
	if rs.has("/api/gate/check") {
		t.Fatal("gate checked on zero-stack apply")
	}
	if !rs.has("/api/init") {
		t.Fatal("expected Init even with zero stacks")
	}
}

// TestApplyClassifiesBeforeGate asserts the self-sufficient ordering: with a
// changed stack, apply submits the classify Finalize (re-establishing the gate's
// classification + grant requests, keyed to pr/env) BEFORE it checks the gate.
// This is what recovers a stranded merged PR after a serve restart.
func TestApplyClassifiesBeforeGate(t *testing.T) {
	srv, rs := stubServer(t)
	setApplyEnv(t, srv.URL, 331, "test")
	withFakeTM(t, &fakeTM{changed: []string{"cluster/fh-test"}}, nil)

	_ = runApply([]string{"--dir", t.TempDir()})

	if !rs.orderedBefore("/api/finalize", "/api/gate/check") {
		t.Fatal("apply must finalize (classify) before checking the gate")
	}
}

// TestApplyFailureFinalizes asserts the always-terminal-Finalize invariant: when
// the apply script fails, apply emits Finalize{Failed:true} (so the check run
// concludes failure with per-stack attribution) and exits 1.
func TestApplyFailureFinalizes(t *testing.T) {
	srv, rs := stubServer(t)
	setApplyEnv(t, srv.URL, 331, "test")
	withFakeTM(t, &fakeTM{changed: []string{"cluster/fh-test"}, scriptErr: errBoom}, nil)

	code := runApply([]string{"--dir", t.TempDir()})
	if code != 1 {
		t.Fatalf("exit=%d want 1", code)
	}
	if !rs.finalizeFailed() {
		t.Fatal("expected Finalize{Failed:true} on apply failure")
	}
}

// TestApplyStateMoveFailureFinalizes asserts a failed cross-state move pre-phase
// is surfaced to the apply check run as a Finalize{Failed:true} carrying the
// cause, and apply exits 1 without running the apply script.
func TestApplyStateMoveFailureFinalizes(t *testing.T) {
	srv, rs := stubServer(t)
	setApplyEnv(t, srv.URL, 331, "test")
	f := &fakeTM{changed: []string{"cluster/fh-test"}}
	withFakeTM(t, f, nil)

	orig := applyMovesFn
	applyMovesFn = func(_ context.Context, _ string, _ bool, _ statemove.Locker, _ io.Writer) error {
		return errBoom
	}
	t.Cleanup(func() { applyMovesFn = orig })

	code := runApply([]string{"--dir", t.TempDir()})
	if code != 1 {
		t.Fatalf("exit=%d want 1", code)
	}
	fin, ok := rs.lastFinalize()
	if !ok || !fin.Failed {
		t.Fatal("expected a terminal Finalize{Failed:true} on state-move failure")
	}
	if !strings.Contains(fin.ReportMarkdown, "cross-state move failed") {
		t.Errorf("state-move Finalize report = %q, want it to mention the move failure", fin.ReportMarkdown)
	}
	if f.scriptRan {
		t.Error("apply script ran despite a failed state-move pre-phase")
	}
}

// TestApplyHappyPathOrder locks in the full self-healing ordering on the happy
// path: init → finalize(classify) → gate/check → finalize(terminal) → gate/revoke,
// and exit 0 when the gate approves.
func TestApplyHappyPathOrder(t *testing.T) {
	srv, rs := stubServer(t)
	setApplyEnv(t, srv.URL, 200, "prod")
	withFakeTM(t, &fakeTM{changed: []string{"cluster/fh-prod"}}, nil)

	code := runApply([]string{"--dir", t.TempDir()})
	if code != 0 {
		t.Fatalf("exit=%d want 0", code)
	}
	for _, pair := range [][2]string{
		{"/api/init", "/api/finalize"},
		{"/api/finalize", "/api/gate/check"},
		{"/api/gate/check", "/api/gate/revoke"},
	} {
		if !rs.orderedBefore(pair[0], pair[1]) {
			t.Errorf("expected %s before %s; order=%v", pair[0], pair[1], rs.order)
		}
	}
}

// TestApplyFinalizeCarriesCounts asserts the classify-pass Finalize carries the
// per-stack op counts returned by classifyForGateFn. The classify-pass Finalize
// (the one with Gates/Categories set, emitted before the gate check) must have
// Counts["a"].Add == 3 when the stub returns that value.
func TestApplyFinalizeCarriesCounts(t *testing.T) {
	srv, rs := stubServer(t)
	setApplyEnv(t, srv.URL, 400, "staging")
	withFakeTM(t, &fakeTM{changed: []string{"stacks/a"}}, nil)

	// Override classifyForGateFn (installed by withFakeTM) with one that
	// returns a non-empty Counts map so we can assert it ends up in the Finalize.
	origCls := classifyForGateFn
	classifyForGateFn = func(_ context.Context, _ string, _ []string, _ string, _ bool, _ string) (
		[]events.GateTarget, map[string][]events.Category, map[string]events.Counts, []string, string, error,
	) {
		return nil, map[string][]events.Category{}, map[string]events.Counts{"a": {Add: 3}}, nil, "classified", nil
	}
	t.Cleanup(func() { classifyForGateFn = origCls })

	code := runApply([]string{"--dir", t.TempDir()})
	if code != 0 {
		t.Fatalf("exit=%d want 0", code)
	}

	// The classify-pass Finalize is the one with non-empty Counts (it comes
	// before the terminal Finalize{ID, Failed:...}).
	rs.mu.Lock()
	defer rs.mu.Unlock()
	var classifyFin *events.Finalize
	for i := range rs.finalizes {
		if len(rs.finalizes[i].Counts) > 0 {
			f := rs.finalizes[i]
			classifyFin = &f
			break
		}
	}
	if classifyFin == nil {
		t.Fatal("no classify-pass Finalize with Counts found; classify-pass Finalize must carry Counts")
	}
	if got := classifyFin.Counts["a"].Add; got != 3 {
		t.Errorf("classify-pass Finalize.Counts[\"a\"].Add = %d, want 3", got)
	}
}

// TestPrintGateRejectedClassifies asserts the fail-closed gate rejection prints
// a classified, actionable message: a not-classified/awaiting gate points the
// operator at the live URL + "approve" + "re-run"; an unreachable serve points
// at the break-glass runbook.
func TestPrintGateRejectedClassifies(t *testing.T) {
	t.Setenv(runner.EnvServer, "https://serve.example")

	var awaiting strings.Builder
	printGateRejected(&awaiting, fmt.Errorf("apply gate not satisfied (fail-closed): post /api/gate/check: 409: not classified"), runner.ClientFromEnv(), 331)
	got := awaiting.String()
	for _, want := range []string{"AWAITING_APPROVAL", "https://serve.example", "approve", "re-run", "#331"} {
		if !strings.Contains(got, want) {
			t.Errorf("awaiting message missing %q in:\n%s", want, got)
		}
	}

	var down strings.Builder
	printGateRejected(&down, fmt.Errorf("apply gate not satisfied (fail-closed): post /api/gate/check: connection refused"), runner.ClientFromEnv(), 331)
	if !strings.Contains(down.String(), "GATE_UNREACHABLE") || !strings.Contains(down.String(), "break-glass") {
		t.Errorf("unreachable message missing break-glass guidance:\n%s", down.String())
	}
}
