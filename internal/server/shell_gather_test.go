package server

import (
	"context"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/reconcile"
)

// gather no longer maps flat gate_targets rows (mapRawGate is gone) — it replays
// the event stream through Evolve. These tests assert the gate sum type is
// reconstructed losslessly by driving a real signal flow and re-gathering. The
// per-variant Evolve mapping is unit-tested in internal/reconcile; here we cover
// the shell's append→replay round-trip end to end.

// A finalize that establishes gate targets, then a full-active re-list, must
// replay to Satisfied with the shared lease intact.
func TestGatherReplaysSatisfied(t *testing.T) {
	app := New(newServerTestDB(t), &MockGitHub{}, Config{})
	fake := approval.NewFake()
	fake.Pool = []string{"sa0", "sa1"}
	app.Approval = fake
	sh := NewShell(app)
	seedInit(t, sh, events.Init{ID: "e1", PR: 7, Environment: "staging", Repo: "r"})

	// Establish the gate (requests grants → Pending).
	if err := sh.Handle(context.Background(), 7, "staging", "r", reconcile.RunnerFinalize{
		Gates: []events.GateTarget{{Class: "iam", Target: "p1"}},
	}); err != nil {
		t.Fatal(err)
	}

	// Re-gather: the prior gate must carry the requested target + lease.
	world, err := sh.gather(7, "staging")
	if err != nil {
		t.Fatal(err)
	}
	if reconcile.Requester(world.Prior.Gate) == "" {
		t.Fatalf("want a leased gate after finalize, got %T %+v", world.Prior.Gate, world.Prior.Gate)
	}

	// Observe all targets ACTIVE → gate promotes to Satisfied; replay it back.
	if err := sh.Handle(context.Background(), 7, "staging", "r", reconcile.GateTick{
		Grants: []reconcile.ObservedGrant{
			{Class: "iam", Target: "p1", Name: "g1", State: approval.StateActive, Requester: reconcile.Requester(world.Prior.Gate)},
		},
	}); err != nil {
		t.Fatal(err)
	}
	w2, err := sh.gather(7, "staging")
	if err != nil {
		t.Fatal(err)
	}
	sat, ok := w2.Prior.Gate.(reconcile.Satisfied)
	if !ok {
		t.Fatalf("want Satisfied after all-ACTIVE re-list, got %T", w2.Prior.Gate)
	}
	if len(sat.Targets) != 1 || sat.Targets[0].Target != "p1" {
		t.Fatalf("bad replayed targets: %+v", sat.Targets)
	}
}

// An empty stream (never finalized) replays to NotClassified at version 0.
func TestGatherReplaysNotClassifiedForEmptyStream(t *testing.T) {
	sh := newTestShell(t)
	world, err := sh.gather(7, "staging")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := world.Prior.Gate.(reconcile.NotClassified); !ok {
		t.Fatalf("want NotClassified for empty stream, got %T", world.Prior.Gate)
	}
}

// A clean finalize (no gate targets) replays to Clean.
func TestGatherReplaysCleanForGatelessFinalize(t *testing.T) {
	sh := newTestShell(t)
	if err := sh.Handle(context.Background(), 7, "staging", "r", reconcile.RunnerFinalize{}); err != nil {
		t.Fatal(err)
	}
	world, err := sh.gather(7, "staging")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := world.Prior.Gate.(reconcile.Clean); !ok {
		t.Fatalf("want Clean for gateless finalize, got %T", world.Prior.Gate)
	}
}
