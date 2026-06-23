package server

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

func TestSweepSkipsPRWithoutExecutionRepo(t *testing.T) {
	// A PR with an open grant but no execution row → OpenGrantPRs yields repo "".
	// The sweep must skip it (can't ask GitHub) without calling PRAbandoned or revoking.
	db := newServerTestDB(t)
	called := false
	gh := &MockGitHub{
		PRAbandonedFn: func(context.Context, string, int) (bool, error) { called = true; return true, nil },
	}
	a := New(db, gh, Config{})
	a.Approval = approval.NewFake()
	if err := store.UpsertTarget(db, 7, "staging", "iam", "p1", "g1", "ACTIVE"); err != nil {
		t.Fatal(err)
	}

	a.sweepOrphanedGrants(context.Background())

	if called {
		t.Fatal("sweep must skip a PR with no execution repo without calling PRAbandoned")
	}
	if st := gateTargetState(t, a); st != "ACTIVE" {
		t.Fatalf("grant must be untouched on empty-repo skip, got %q", st)
	}
}

// setupActiveGate drives PR 7 / staging to a stored ACTIVE grant via the real
// init → finalize → approve → tick flow, with gh.PRAbandoned stubbed by abandoned.
func setupActiveGate(t *testing.T, abandoned func(context.Context, string, int) (bool, error)) *App {
	t.Helper()
	db := newServerTestDB(t)
	fake := approval.NewFake()
	fake.Pool = []string{"sa0"}
	gh := &MockGitHub{
		CreateCheckRunFn: func(context.Context, string, string, string, string) (int64, error) { return 1, nil },
		UpdateCheckRunFn: func(context.Context, string, int64, CheckRunUpdate) error { return nil },
		PRAbandonedFn:    abandoned,
	}
	a := New(db, gh, Config{})
	a.Approval = fake
	srv := httptest.NewServer(a.Routes())
	t.Cleanup(srv.Close)

	post(t, srv, "/api/init", events.Init{ID: "e1", Repo: "o/r", SHA: "sha", PR: 7, Environment: "staging",
		Stacks: []events.StackState{{Path: "stacks/a", Project: "proj-a", Status: events.StatusPlanned}}})
	post(t, srv, "/api/finalize", events.Finalize{ID: "e1", ReportMarkdown: "# r",
		Gates: []events.GateTarget{{Class: "iam", Target: "proj-a"}}})
	fake.Approve(approval.Request{Class: "iam", Target: "proj-a", PR: 7, Environment: "staging"})
	if err := a.shell.tick(context.Background(), 7, "staging"); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if st := gateTargetState(t, a); st != "ACTIVE" {
		t.Fatalf("setup: want ACTIVE target before sweep, got %q", st)
	}
	return a
}

func gateTargetState(t *testing.T, a *App) string {
	t.Helper()
	ts, err := store.TargetsFor(a.db, 7, "staging")
	if err != nil || len(ts) != 1 {
		t.Fatalf("TargetsFor: %v %+v", err, ts)
	}
	return ts[0].State
}

func TestSweepRevokesAbandonedPRGrant(t *testing.T) {
	a := setupActiveGate(t, func(context.Context, string, int) (bool, error) { return true, nil })
	a.sweepOrphanedGrants(context.Background())
	if st := gateTargetState(t, a); st != "REVOKED" {
		t.Fatalf("closed-PR orphan not revoked: target state = %q, want REVOKED", st)
	}
}

func TestSweepKeepsOpenPRGrant(t *testing.T) {
	a := setupActiveGate(t, func(context.Context, string, int) (bool, error) { return false, nil })
	a.sweepOrphanedGrants(context.Background())
	if st := gateTargetState(t, a); st != "ACTIVE" {
		t.Fatalf("open-PR grant must be untouched: target state = %q, want ACTIVE", st)
	}
}

func TestSweepKeepsGrantOnPRAbandonedError(t *testing.T) {
	a := setupActiveGate(t, func(context.Context, string, int) (bool, error) { return false, fmt.Errorf("github down") })
	a.sweepOrphanedGrants(context.Background())
	if st := gateTargetState(t, a); st != "ACTIVE" {
		t.Fatalf("on PRAbandoned error the grant must be kept: target state = %q, want ACTIVE", st)
	}
}

func TestSweepKeepsMergedPRGrant(t *testing.T) {
	// A merged PR (not abandoned) with an open grant must NOT be swept — its
	// post-merge apply needs the grant. PRAbandoned returns false for merged.
	a := setupActiveGate(t, func(context.Context, string, int) (bool, error) { return false, nil })
	a.sweepOrphanedGrants(context.Background())
	if st := gateTargetState(t, a); st != "ACTIVE" {
		t.Fatalf("merged-PR grant must be untouched: target state = %q, want ACTIVE", st)
	}
}
