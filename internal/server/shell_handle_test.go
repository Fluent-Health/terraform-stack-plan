package server

import (
	"context"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/reconcile"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

func TestHandleFinalizeRequestsAllGatesViaFixpoint(t *testing.T) {
	app := New(newServerTestDB(t), &MockGitHub{}, Config{})
	fake := approval.NewFake()
	fake.Pool = []string{"sa0", "sa1"} // enable pool leasing so a requester is assigned
	app.Approval = fake
	sh := NewShell(app)

	// Seed an execution so renders have something to drive.
	if err := store.UpsertInit(app.db, events.Init{ID: "e1", PR: 7, Environment: "staging", Repo: "r"}); err != nil {
		t.Fatal(err)
	}

	err := sh.Handle(context.Background(), 7, "staging", "r", reconcile.RunnerFinalize{
		Gates: []events.GateTarget{{Class: "iam", Target: "p1"}, {Class: "iam", Target: "p2"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// After the fixpoint, BOTH targets must be persisted with a grant + shared lease.
	targets, err := store.TargetsFor(app.db, 7, "staging")
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 {
		t.Fatalf("want 2 persisted targets, got %d", len(targets))
	}
	if targets[0].Requester == "" || targets[0].Requester != targets[1].Requester {
		t.Fatalf("want both targets sharing one lease, got %q/%q", targets[0].Requester, targets[1].Requester)
	}
}
