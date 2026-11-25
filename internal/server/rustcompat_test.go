package server

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
	"github.com/Fluent-Health/terraform-stack-plan/internal/claims"
	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/execution"
	"github.com/Fluent-Health/terraform-stack-plan/internal/reconcile"
)

// TestWriteRustCompatDB drives the real shell through every aggregate and copies
// the resulting database to $TFSTACKPLAN_RUST_COMPAT_DB, for the Rust store's
// compatibility test (rust/store/tests/go_compat.rs) to open, replay and compare
// against the projections Go wrote. It is skipped unless that variable is set.
func TestWriteRustCompatDB(t *testing.T) {
	out := os.Getenv("TFSTACKPLAN_RUST_COMPAT_DB")
	if out == "" {
		t.Skip("TFSTACKPLAN_RUST_COMPAT_DB not set")
	}
	db := newServerTestDB(t)
	app := New(db, &MockGitHub{}, Config{})
	fake := approval.NewFake()
	fake.Pool = []string{"sa0", "sa1"}
	app.Approval = fake
	sh := NewShell(app)
	ctx := context.Background()
	handle := func(pr int, env string, sig reconcile.Signal) {
		t.Helper()
		if err := sh.Handle(ctx, pr, env, "o/r", sig); err != nil {
			t.Fatalf("handle pr=%d env=%s %T: %v", pr, env, sig, err)
		}
	}
	exec := func(id string, sig execution.Signal) {
		t.Helper()
		if err := sh.HandleExec(ctx, id, sig); err != nil {
			t.Fatalf("exec %s %T: %v", id, sig, err)
		}
	}
	tick := func(pr int, env, class, target string) {
		t.Helper()
		grants, _ := fake.ListGrants(ctx, class, target)
		var obs []reconcile.ObservedGrant
		for _, g := range grants {
			if g.Request.PR == pr && g.Request.Environment == env {
				obs = append(obs, reconcile.ObservedGrant{Class: class, Target: target, Name: g.Name, State: g.State, Requester: g.Requester})
			}
		}
		handle(pr, env, reconcile.GateTick{Grants: obs})
	}
	pct := 40

	// PR 7 staging: a gated plan, approved, applied.
	seedInit(t, sh, events.Init{ID: "e7", Repo: "o/r", SHA: "sha7", PR: 7, Environment: "staging", Context: "plan/staging",
		Stacks: []events.StackState{{Path: "stacks/a", Project: "p1", Status: events.StatusPending}, {Path: "stacks/b", Project: "p2", Status: events.StatusPending}, {Path: "stacks/c", Status: events.StatusPending}},
		Edges:  []events.Edge{{From: "stacks/a", To: "stacks/b"}}})
	seedPhase(t, sh, events.PhaseEvent{ID: "e7", Phase: events.PhasePlanning, Label: "planning 1/3", ProgressPct: &pct})
	exec("e7", execution.ReportTick{Stack: "stacks/a", Status: events.StatusPlanned})
	exec("e7", execution.ReportTick{Stack: "stacks/b", Status: events.StatusPlanned, Detail: "2 to add"})
	exec("e7", execution.ReportAnnotate{
		Projects:   map[string]string{"stacks/c": "p3"},
		Categories: map[string][]events.Category{"stacks/a": {{Name: "iam", Icon: "🔐", Attributes: map[string][]string{"role": {"owner"}}}}},
		Counts:     map[string]events.Counts{"stacks/a": {Add: 1, Change: 2}},
		Moving:     []string{"stacks/c"},
	})
	seedPhase(t, sh, events.PhaseEvent{ID: "e7", Phase: events.PhaseReport})
	exec("e7", execution.ReportSucceed{})
	handle(7, "staging", reconcile.RunnerFinalize{Gates: []events.GateTarget{{Class: "iam", Target: "p1"}, {Class: "iam", Target: "p2"}}})
	fake.Approve(approval.Request{Class: "iam", Target: "p1", PR: 7, Environment: "staging"})
	tick(7, "staging", "iam", "p1")
	fake.Approve(approval.Request{Class: "iam", Target: "p2", PR: 7, Environment: "staging"})
	tick(7, "staging", "iam", "p2")

	// PR 8 staging: gated, then the PR closes.
	seedInit(t, sh, events.Init{ID: "e8", Repo: "o/r", SHA: "sha8", PR: 8, Environment: "staging", Context: "plan/staging",
		Stacks: []events.StackState{{Path: "stacks/d", Project: "p4", Status: events.StatusRunning}}})
	exec("e8", execution.ReportFail{})
	handle(8, "staging", reconcile.RunnerFinalize{Gates: []events.GateTarget{{Class: "iam", Target: "p4"}}})
	handle(8, "staging", reconcile.PRClosed{})

	// PR 9 prod: a clean plan, a re-plan that supersedes it, then an apply.
	seedInit(t, sh, events.Init{ID: "e9a", Repo: "o/r", SHA: "sha9a", PR: 9, Environment: "prod", Context: "plan/prod",
		Stacks: []events.StackState{{Path: "stacks/e", Status: events.StatusPending}}})
	handle(9, "prod", reconcile.RunnerFinalize{})
	seedInit(t, sh, events.Init{ID: "e9b", Repo: "o/r", SHA: "sha9b", PR: 9, Environment: "prod", Context: "plan/prod",
		Stacks: []events.StackState{{Path: "stacks/e", Status: events.StatusPending}}})
	exec("e9a", execution.ReportSupersede{By: "e9b"})
	handle(9, "prod", reconcile.RunnerFinalize{Gates: []events.GateTarget{{Class: "iam", Target: "p5"}}})
	handle(9, "prod", reconcile.RunnerFinalize{Gates: []events.GateTarget{{Class: "iam", Target: "p5"}, {Class: "net", Target: "p6"}}, ApplyContext: true})

	// PR 10 nonprod: serve-driven runs with no executor, so every start fails.
	handle(10, "nonprod", reconcile.RunRequested{Kind: reconcile.RunKindPlan, SHA: "abcdef1234567890", Branch: "feat/x"})
	handle(10, "nonprod", reconcile.RunRequested{Kind: reconcile.RunKindPlan, SHA: "abcdef1234567890", Branch: "feat/x", Rerun: true})
	handle(10, "nonprod", reconcile.RunRequested{Kind: reconcile.RunKindApply, SHA: "fedcba0987654321", Branch: "main"})
	handle(10, "nonprod", reconcile.InboundBuild{Kind: reconcile.RunKindPlan, SHA: "abcdef1234567890", BuildRef: "build-1"})

	// The staging claim ledger: two PRs, a renewal and a partial release.
	now := time.Date(2026, 6, 24, 12, 0, 0, 123456789, time.UTC)
	for _, cmd := range []claims.Command{
		claims.AcquireClaim{PR: 7, Stacks: []string{"stacks/a", "stacks/b"}, Now: now},
		claims.AcquireClaim{PR: 11, Stacks: []string{"stacks/x"}, Now: now.Add(time.Minute)},
		claims.RenewClaim{PR: 7, Now: now.Add(10 * time.Minute)},
		claims.ReleaseClaimStack{PR: 7, Stack: "stacks/b"},
	} {
		if err := sh.handleClaim("staging", cmd); err != nil {
			t.Fatal(err)
		}
	}
	if err := sh.handleClaim("prod", claims.AcquireClaim{PR: 9, Stacks: []string{"stacks/e"}, Now: now}); err != nil {
		t.Fatal(err)
	}
	// The apply of PR 7 releases its claims and its gate.
	handle(7, "staging", reconcile.ApplySucceeded{})

	if _, err := db.Exec(`VACUUM INTO ?`, out); err != nil {
		t.Fatalf("copy database: %v", err)
	}
}
