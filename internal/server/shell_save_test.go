package server

import (
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
	"github.com/Fluent-Health/terraform-stack-plan/internal/reconcile"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// project writes the gate_targets projection from a folded ChangeSet: it upserts
// the desired targets (with requester) and prunes any persisted target the new
// state no longer carries. Asserted via the projection itself (store.TargetsFor),
// since project no longer touches the event stream that gather replays.
func TestProjectPersistsTargetsAndPrunes(t *testing.T) {
	app := New(newServerTestDB(t), &MockGitHub{}, Config{})
	sh := NewShell(app)

	// Pre-seed TWO persisted targets for (7, staging).
	if err := store.UpsertTarget(app.db, 7, "staging", "iam", "p1", "g1", "ACTIVE"); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertTarget(app.db, 7, "staging", "iam", "p2", "g2", "ACTIVE"); err != nil {
		t.Fatal(err)
	}

	// Project a state that carries only p1 → p2 must be pruned.
	cs := reconcile.ChangeSet{PR: 7, Environment: "staging", Gate: reconcile.Satisfied{
		Lease:   reconcile.Lease{Requester: "sa3"},
		Targets: []reconcile.Target{{Class: "iam", Target: "p1", GrantName: "g1", Grant: approval.StateActive}},
	}}
	if err := sh.project(cs); err != nil {
		t.Fatal(err)
	}

	targets, err := store.TargetsFor(app.db, 7, "staging")
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].Target != "p1" {
		t.Fatalf("want only p1 retained (p2 pruned), got %+v", targets)
	}
	if targets[0].Requester != "sa3" {
		t.Fatalf("want requester sa3 persisted, got %q", targets[0].Requester)
	}
}
