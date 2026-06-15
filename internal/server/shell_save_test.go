package server

import (
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
	"github.com/Fluent-Health/terraform-stack-plan/internal/reconcile"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

func TestSaveGatePersistsTargetsAndPrunes(t *testing.T) {
	app := New(newServerTestDB(t), &MockGitHub{}, Config{})
	sh := NewShell(app)

	// Pre-seed TWO persisted targets for (7, staging).
	if err := store.UpsertTarget(app.db, 7, "staging", "iam", "p1", "g1", "ACTIVE"); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertTarget(app.db, 7, "staging", "iam", "p2", "g2", "ACTIVE"); err != nil {
		t.Fatal(err)
	}

	// Save a state that carries only p1 → p2 must be pruned.
	cs := reconcile.ChangeSet{PR: 7, Environment: "staging", Gate: reconcile.Satisfied{
		Lease:   reconcile.Lease{Requester: "sa3"},
		Targets: []reconcile.Target{{Class: "iam", Target: "p1", GrantName: "g1", Grant: approval.StateActive}},
	}}
	if err := sh.save(cs); err != nil {
		t.Fatal(err)
	}

	reloaded, err := sh.gather(7, "staging")
	if err != nil {
		t.Fatal(err)
	}
	got, ok := reloaded.Prior.Gate.(reconcile.Satisfied) // single all-ACTIVE target maps to Satisfied
	if !ok {
		t.Fatalf("want Satisfied after reload, got %T", reloaded.Prior.Gate)
	}
	if len(got.Targets) != 1 || got.Targets[0].Target != "p1" {
		t.Fatalf("want only p1 retained (p2 pruned), got %+v", got.Targets)
	}
	if got.Lease.Requester != "sa3" {
		t.Fatalf("want requester sa3 persisted, got %q", got.Lease.Requester)
	}
}
