package server

import (
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/reconcile"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

func stackStatus(t *testing.T, sh *Shell, execID, path string) string {
	t.Helper()
	g, err := store.LoadGraph(sh.app.db, execID)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range g.Stacks {
		if s.Path == path {
			return string(s.Status)
		}
	}
	t.Fatalf("stack %q not found", path)
	return ""
}

func seedExec(t *testing.T, sh *Shell) {
	t.Helper()
	// One gated stack (project p1) + one ungated stack (project other) + one failed.
	seedInit(t, sh, events.Init{
		ID: "e1", PR: 7, Environment: "staging", Repo: "r",
		Stacks: []events.StackState{
			{Path: "s1", Project: "p1", Status: events.StatusPlanned},
			{Path: "s2", Project: "other", Status: events.StatusPlanned},
			{Path: "s3", Project: "p1", Status: events.StatusFailed},
		},
	})
}

func TestSaveWritesGatedOverlay(t *testing.T) {
	app := New(newServerTestDB(t), &MockGitHub{}, Config{})
	sh := NewShell(app)
	seedExec(t, sh)

	// Pending gate on p1 → s1 (project p1, planned) becomes gated; s2 (other) stays
	// planned; s3 (p1 but failed) stays failed.
	cs := reconcile.ChangeSet{PR: 7, Environment: "staging", Gate: reconcile.Pending{
		Targets: []reconcile.Target{{Class: "iam", Target: "p1", GrantName: "g1"}},
	}}
	if err := sh.project(cs, nil); err != nil {
		t.Fatal(err)
	}
	if got := stackStatus(t, sh, "e1", "s1"); got != "gated" {
		t.Fatalf("s1 want gated, got %q", got)
	}
	if got := stackStatus(t, sh, "e1", "s2"); got != "planned" {
		t.Fatalf("s2 want planned (ungated), got %q", got)
	}
	if got := stackStatus(t, sh, "e1", "s3"); got != "failed" {
		t.Fatalf("s3 want failed (failed wins), got %q", got)
	}
}

func TestSaveWritesSafeOverlayWhenSatisfied(t *testing.T) {
	app := New(newServerTestDB(t), &MockGitHub{}, Config{})
	sh := NewShell(app)
	seedExec(t, sh)
	cs := reconcile.ChangeSet{PR: 7, Environment: "staging", Gate: reconcile.Satisfied{
		Targets: []reconcile.Target{{Class: "iam", Target: "p1", GrantName: "g1", Grant: approval.StateActive}},
	}}
	if err := sh.project(cs, nil); err != nil {
		t.Fatal(err)
	}
	if got := stackStatus(t, sh, "e1", "s1"); got != "safe" {
		t.Fatalf("s1 want safe, got %q", got)
	}
	if got := stackStatus(t, sh, "e1", "s3"); got != "failed" {
		t.Fatalf("s3 want failed (failed wins), got %q", got)
	}
}

// seedExecWithAbortedAndMoving mirrors seedExec but adds two more gate-target
// (project p1) stacks: one exec-terminal `aborted`, one `moving`. Used to pin
// the overlay skip set at (failed, aborted) — aborted must not be clobbered,
// same as failed; moving must NOT be in the skip set so gated/safe wins over
// it (restoring the old gated-wins-over-moving precedence).
func seedExecWithAbortedAndMoving(t *testing.T, sh *Shell) {
	t.Helper()
	seedInit(t, sh, events.Init{
		ID: "e1", PR: 7, Environment: "staging", Repo: "r",
		Stacks: []events.StackState{
			{Path: "s1", Project: "p1", Status: events.StatusPlanned},
			{Path: "s2", Project: "other", Status: events.StatusPlanned},
			{Path: "s3", Project: "p1", Status: events.StatusFailed},
			{Path: "s4", Project: "p1", Status: events.StatusAborted},
			{Path: "s5", Project: "p1", Status: events.StatusMoving},
		},
	})
}

// TestSaveOverlaySkipsAborted pins problem 1: aborted is exec-terminal (set by
// the execution aggregate on innocent stacks at a failed finalize) and must
// not be clobbered by the gate overlay, the same way failed is not.
func TestSaveOverlaySkipsAborted(t *testing.T) {
	app := New(newServerTestDB(t), &MockGitHub{}, Config{})
	sh := NewShell(app)
	seedExecWithAbortedAndMoving(t, sh)

	pending := reconcile.ChangeSet{PR: 7, Environment: "staging", Gate: reconcile.Pending{
		Targets: []reconcile.Target{{Class: "iam", Target: "p1", GrantName: "g1"}},
	}}
	if err := sh.project(pending, nil); err != nil {
		t.Fatal(err)
	}
	if got := stackStatus(t, sh, "e1", "s4"); got != "aborted" {
		t.Fatalf("s4 want aborted (aborted wins over gated), got %q", got)
	}

	satisfied := reconcile.ChangeSet{PR: 7, Environment: "staging", Gate: reconcile.Satisfied{
		Targets: []reconcile.Target{{Class: "iam", Target: "p1", GrantName: "g1", Grant: approval.StateActive}},
	}}
	if err := sh.project(satisfied, nil); err != nil {
		t.Fatal(err)
	}
	if got := stackStatus(t, sh, "e1", "s4"); got != "aborted" {
		t.Fatalf("s4 want aborted (aborted wins over safe), got %q", got)
	}
}

// TestSaveOverlayWinsOverMoving pins problem 2: a gate-target stack that is
// currently `moving` must be overlaid to gated/safe — moving must NOT be in
// the skip set. This restores the pre-Task-8 gated-wins-over-moving
// precedence; it's safe because the overlay UPDATE is scoped to
// `project = t.Target` (gate-target rows only), so a non-gate-target moving
// stack (e.g. s2, project "other") is never touched.
func TestSaveOverlayWinsOverMoving(t *testing.T) {
	app := New(newServerTestDB(t), &MockGitHub{}, Config{})
	sh := NewShell(app)
	seedExecWithAbortedAndMoving(t, sh)

	pending := reconcile.ChangeSet{PR: 7, Environment: "staging", Gate: reconcile.Pending{
		Targets: []reconcile.Target{{Class: "iam", Target: "p1", GrantName: "g1"}},
	}}
	if err := sh.project(pending, nil); err != nil {
		t.Fatal(err)
	}
	if got := stackStatus(t, sh, "e1", "s5"); got != "gated" {
		t.Fatalf("s5 want gated (gated wins over moving), got %q", got)
	}

	app2 := New(newServerTestDB(t), &MockGitHub{}, Config{})
	sh2 := NewShell(app2)
	seedExecWithAbortedAndMoving(t, sh2)
	satisfied := reconcile.ChangeSet{PR: 7, Environment: "staging", Gate: reconcile.Satisfied{
		Targets: []reconcile.Target{{Class: "iam", Target: "p1", GrantName: "g1", Grant: approval.StateActive}},
	}}
	if err := sh2.project(satisfied, nil); err != nil {
		t.Fatal(err)
	}
	if got := stackStatus(t, sh2, "e1", "s5"); got != "safe" {
		t.Fatalf("s5 want safe (safe wins over moving), got %q", got)
	}
}
