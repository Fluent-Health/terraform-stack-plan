package server

import (
	"testing"
	"time"

	"github.com/Fluent-Health/terraform-stack-plan/internal/claims"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// newPRMergeStateTestApp builds an App scoped to env "prod" (this serve's tier).
func newPRMergeStateTestApp(t *testing.T) *App {
	t.Helper()
	db := newServerTestDB(t)
	gh := &recordingGitHub{}
	return New(db, gh, Config{PublicBaseURL: "https://srv", Environment: "prod"})
}

func TestPRMergeStateNoPlan(t *testing.T) {
	a := newPRMergeStateTestApp(t)
	got := a.prMergeState(42)
	if got.Environment != "prod" || got.RequiredCheck != "terraform/prod" {
		t.Fatalf("got = %+v", got)
	}
	if !got.MergeBlocked {
		t.Fatalf("expected MergeBlocked=true (fail-closed, no plan), got %+v", got)
	}
	if got.Blocker == "" {
		t.Fatalf("expected a non-empty Blocker string, got %+v", got)
	}
	if got.CheckConclusion != "" {
		t.Fatalf("expected empty CheckConclusion (no plan), got %q", got.CheckConclusion)
	}
}

func TestPRMergeStateClear(t *testing.T) {
	a := newPRMergeStateTestApp(t)
	seedPlan(t, a.db, 7, "prod", "o/r", "sha1", []string{"a", "b"})
	if err := store.SetReport(a.db, "sha1-prod", "# report"); err != nil {
		t.Fatal(err)
	}
	got := a.prMergeState(7)
	if got.MergeBlocked {
		t.Fatalf("expected MergeBlocked=false, got %+v", got)
	}
	if got.Blocker != "" {
		t.Fatalf("expected empty Blocker on clear, got %q", got.Blocker)
	}
	if got.CheckConclusion != "success" {
		t.Fatalf("expected CheckConclusion=success, got %q", got.CheckConclusion)
	}
}

func TestPRMergeStateHeld(t *testing.T) {
	a := newPRMergeStateTestApp(t)
	seedPlan(t, a.db, 7, "prod", "o/r", "sha1", []string{"a", "b"})
	if err := store.SetReport(a.db, "sha1-prod", "# report"); err != nil {
		t.Fatal(err)
	}
	// Another PR (5) holds stack "a" in prod => held for PR 7.
	if err := a.shell.handleClaim("prod", claims.AcquireClaim{PR: 5, Stacks: []string{"a"}, Now: time.Now()}); err != nil {
		t.Fatal(err)
	}
	got := a.prMergeState(7)
	if !got.MergeBlocked {
		t.Fatalf("expected MergeBlocked=true (held), got %+v", got)
	}
	if got.Blocker == "" {
		t.Fatalf("expected non-empty Blocker, got %+v", got)
	}
	if got.CheckConclusion != "" {
		t.Fatalf("expected CheckConclusion=\"\" (in progress) while held, got %q", got.CheckConclusion)
	}
}
