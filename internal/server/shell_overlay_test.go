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
	err := store.UpsertInit(sh.app.db, events.Init{
		ID: "e1", PR: 7, Environment: "staging", Repo: "r",
		Stacks: []events.StackState{
			{Path: "s1", Project: "p1", Status: events.StatusPlanned},
			{Path: "s2", Project: "other", Status: events.StatusPlanned},
			{Path: "s3", Project: "p1", Status: events.StatusFailed},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
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
	if err := sh.save(cs); err != nil {
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
	if err := sh.save(cs); err != nil {
		t.Fatal(err)
	}
	if got := stackStatus(t, sh, "e1", "s1"); got != "safe" {
		t.Fatalf("s1 want safe, got %q", got)
	}
	if got := stackStatus(t, sh, "e1", "s3"); got != "failed" {
		t.Fatalf("s3 want failed (failed wins), got %q", got)
	}
}
