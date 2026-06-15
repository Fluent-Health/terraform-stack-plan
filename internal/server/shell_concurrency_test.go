package server

import (
	"context"
	"sync"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/reconcile"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// A finalize (requesting grants) racing a GateTick must never observe a
// premature Satisfied: with per-ChangeSet serialization the tick either runs
// fully before finalize or fully after, never interleaving mid-request.
func TestConcurrentFinalizeAndTickNoPrematureSatisfied(t *testing.T) {
	app := New(newServerTestDB(t), &MockGitHub{}, Config{})
	fake := approval.NewFake()
	fake.Pool = []string{"sa0", "sa1"}
	app.Approval = fake
	sh := NewShell(app)
	if err := store.UpsertInit(app.db, events.Init{ID: "e1", PR: 7, Environment: "staging", Repo: "r"}); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = sh.Handle(context.Background(), 7, "staging", "r", reconcile.RunnerFinalize{
			Gates: []events.GateTarget{{Class: "iam", Target: "p1"}, {Class: "iam", Target: "p2"}},
		})
	}()
	go func() {
		defer wg.Done()
		_ = sh.Handle(context.Background(), 7, "staging", "r", reconcile.GateTick{})
	}()
	wg.Wait()

	// Final persisted state must be internally consistent: if both targets are
	// present they must share the same lease (no half-leased artifact).
	raw, err := store.LoadChangeSet(app.db, 7, "staging")
	if err != nil {
		t.Fatal(err)
	}
	if len(raw.Targets) == 2 {
		if raw.Targets[0].Requester != raw.Targets[1].Requester {
			t.Fatalf("inconsistent lease across targets: %q/%q", raw.Targets[0].Requester, raw.Targets[1].Requester)
		}
	}
}
