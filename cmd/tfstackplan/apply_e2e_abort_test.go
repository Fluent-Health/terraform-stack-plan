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

// buildTFStackplan compiles the tfstackplan binary into dir/tfstackplan and
// returns the path. It is cached for the process lifetime via sync.Once.
var (
	builtBinOnce sync.Once
	builtBinPath string
	builtBinErr  error
)

func tfstackplanBinary(t *testing.T) string {
	t.Helper()
	builtBinOnce.Do(func() {
		tmp, err := os.MkdirTemp("", "tfstackplan-bin-*")
		if err != nil {
			builtBinErr = err
			return
		}
		out := filepath.Join(tmp, "tfstackplan")
		// Build from the module import path; works from any CWD inside the module.
		cmd := exec.Command("go", "build", "-o", out, "github.com/Fluent-Health/terraform-stack-plan/cmd/tfstackplan")
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if b, err := cmd.CombinedOutput(); err != nil {
			builtBinErr = err
			_ = os.RemoveAll(tmp)
			_ = b
			return
		}
		builtBinPath = out
	})
	if builtBinErr != nil {
		t.Fatalf("build tfstackplan: %v", builtBinErr)
	}
	return builtBinPath
}

// abortFixture sets up a git-initialized copy of testdata/abortfixture with:
//   - a stub terraform on PATH that fails for stacks/fail and returns a
//     "0 added, 0 changed, 0 destroyed" summary for stacks/nochange
//   - the tfstackplan binary on PATH (so the fixture's apply script can invoke
//     `tfstackplan run step` to tick per-stack outcomes in-process)
//
// Returns the fixture root dir. Skips when terramate isn't runnable.
func abortFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS("testdata/abortfixture")); err != nil {
		t.Fatal(err)
	}
	probe := exec.Command("terramate", "version")
	probe.Dir = dir
	if out, err := probe.CombinedOutput(); err != nil {
		t.Skipf("terramate not runnable: %v: %s", err, out)
	}
	for _, a := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@t"},
		{"config", "user.name", "t"},
		{"config", "commit.gpgsign", "false"},
		{"add", "-A"},
		{"commit", "-qm", "init"},
	} {
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

	// Stub terraform: fail for stacks/fail, nochange for stacks/nochange.
	// The script inspects $PWD (terramate runs each script from the stack dir).
	const stubTF = `#!/bin/sh
case "$PWD" in
  */stacks/fail)
    echo "Error: Apply failed." >&2
    exit 1
    ;;
  */stacks/nochange)
    echo "Apply complete! Resources: 0 added, 0 changed, 0 destroyed."
    exit 0
    ;;
  *)
    echo "Apply complete! Resources: 1 added, 0 changed, 0 destroyed."
    exit 0
    ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "terraform"), []byte(stubTF), 0o755); err != nil {
		t.Fatal(err)
	}

	// Symlink (or copy) the compiled tfstackplan binary so the fixture's apply
	// script can invoke `tfstackplan run step`.
	tfsBin := tfstackplanBinary(t)
	if err := os.Symlink(tfsBin, filepath.Join(bin, "tfstackplan")); err != nil {
		// Symlink may fail on some platforms; fall back to a hard-copy.
		data, err2 := os.ReadFile(tfsBin)
		if err2 != nil {
			t.Fatalf("copy tfstackplan: read: %v", err2)
		}
		if err2 = os.WriteFile(filepath.Join(bin, "tfstackplan"), data, 0o755); err2 != nil {
			t.Fatalf("copy tfstackplan: write: %v", err2)
		}
	}

	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

// stackUpdateTracker records the last per-stack Update status emitted by
// `run step` (via /api/update). It is an httptest handler substitute for the
// real server; it records enough state to assert truthful per-stack outcomes
// without needing a live DB.
type stackUpdateTracker struct {
	mu      sync.Mutex
	updates map[string]events.Status // stack path → last ticked status
}

func newStackUpdateTracker() *stackUpdateTracker {
	return &stackUpdateTracker{updates: map[string]events.Status{}}
}

func (tr *stackUpdateTracker) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if r.URL.Path == "/api/update" {
			var u events.Update
			if err := json.Unmarshal(b, &u); err == nil && u.Stack != "" {
				tr.mu.Lock()
				tr.updates[u.Stack] = u.Status
				tr.mu.Unlock()
			}
		}
		w.WriteHeader(200)
	}
}

// lastStatus returns the last Update status recorded for the given stack.
func (tr *stackUpdateTracker) lastStatus(stack string) (events.Status, bool) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	s, ok := tr.updates[stack]
	return s, ok
}

// TestApplyParallelAbortTruthfulStates is the capstone end-to-end test: a
// parallel apply where stacks/fail's stub terraform exits non-zero must produce:
//
//   - stacks/fail    → `failed`   (run step ticks it on non-zero exit)
//   - stacks/nochange → `nochange` (run step sees "0 added, 0 changed, 0 destroyed")
//   - stacks/dependent → never receives a terminal Update (it never ran; the
//     server marks it `aborted` on Finalize{Failed:true})
//
// The key invariant: stacks/nochange must NOT be `failed` — that was the
// blanket-failed bug that `run step` fixes by ticking real outcomes in-process
// before terramate aborts the parallel run.
func TestApplyParallelAbortTruthfulStates(t *testing.T) {
	dir := abortFixture(t)

	tr := newStackUpdateTracker()
	srv := httptest.NewServer(tr.handler())
	defer srv.Close()

	t.Setenv(runner.EnvServer, srv.URL)
	t.Setenv(runner.EnvEnvironment, "staging")
	t.Setenv("TFSTACKPLAN_PR", "7")

	// --parallel 4: stacks/fail and stacks/nochange run concurrently; the
	// after=["/stacks/fail"] on stacks/dependent means it is never started.
	code := runApply([]string{"--dir", dir, "--changed=false", "--parallel", "4"})
	if code != 1 {
		t.Fatalf("run apply (one failing stack) = %d, want 1", code)
	}

	// stacks/fail: run step must emit `failed` (stub terraform exits 1).
	if got, ok := tr.lastStatus("stacks/fail"); !ok || got != events.StatusFailed {
		t.Errorf("stacks/fail: last Update status = (%q, present=%v), want (failed, true)", got, ok)
	}

	// stacks/nochange: run step must emit `nochange` (0/0/0 apply summary).
	if got, ok := tr.lastStatus("stacks/nochange"); !ok || got != events.StatusNochange {
		t.Errorf("stacks/nochange: last Update status = (%q, present=%v), want (nochange, true)", got, ok)
	}

	// The core invariant: nochange must never be `failed`.
	if got, _ := tr.lastStatus("stacks/nochange"); got == events.StatusFailed {
		t.Error("BLANKET-FAILED BUG: stacks/nochange was marked failed — run step must tick the real per-stack outcome")
	}

	// stacks/dependent was never started (blocked on the failing stacks/fail),
	// so run step never ran for it — no terminal Update should exist for it.
	// The server marks it aborted via Finalize{Failed:true}; we assert here that
	// it did NOT receive a false positive Update from run step.
	if got, ok := tr.lastStatus("stacks/dependent"); ok && isTerminal(got) {
		t.Errorf("stacks/dependent: got unexpected terminal Update %q; it should have been aborted server-side, not ticked by run step", got)
	}
}

// isTerminal reports whether s is a terminal (non-transitional) stack status.
func isTerminal(s events.Status) bool {
	switch s {
	case events.StatusFailed, events.StatusSafe, events.StatusNochange,
		events.StatusPlanned, events.StatusGated, events.StatusAborted:
		return true
	}
	return false
}
