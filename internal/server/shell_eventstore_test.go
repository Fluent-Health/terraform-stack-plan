package server

import (
	"context"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/reconcile"
)

// newTestShell wires an App (db + eventStore via New) plus a fresh Shell. The
// eventStore is set inside New from the db, so a Shell built via NewShell(app)
// gets it for free.
func newTestShell(t *testing.T) *Shell {
	t.Helper()
	app := New(newServerTestDB(t), &MockGitHub{}, Config{})
	return NewShell(app)
}

func TestShellAppendsAndReplaysGate(t *testing.T) {
	sh := newTestShell(t)
	ctx := context.Background()

	// A clean (no-gate) finalize: gate should become Clean and be replayable.
	err := sh.Handle(ctx, 7, "nonprod", "repo", reconcile.RunnerFinalize{
		Projects: map[string]string{"a": "proj-a"},
	})
	if err != nil {
		t.Fatalf("handle finalize: %v", err)
	}

	// The stream has events, and gather replays to Clean.
	world, err := sh.gather(7, "nonprod")
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	if world.Version == 0 {
		t.Fatal("expected a non-empty stream after finalize")
	}
	if _, ok := world.Prior.Gate.(reconcile.Clean); !ok {
		t.Fatalf("replayed gate = %T; want Clean", world.Prior.Gate)
	}
}

func TestShellGatherEmptyStreamIsNotClassified(t *testing.T) {
	sh := newTestShell(t)
	world, err := sh.gather(99, "nonprod")
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	if world.Version != 0 {
		t.Fatalf("version = %d; want 0 for empty stream", world.Version)
	}
	if _, ok := world.Prior.Gate.(reconcile.NotClassified); !ok {
		t.Fatalf("empty-stream gate = %T; want NotClassified", world.Prior.Gate)
	}
}
