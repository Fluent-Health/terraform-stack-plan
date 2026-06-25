package store

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

func TestStoreConcurrency(t *testing.T) {
	db := newTestDB(t)

	// Initialize an execution with a single stack
	execID := "concurrent-exec-1"
	initEv := events.Init{
		ID:          execID,
		Repo:        "Fluent-Health/terraform-stack-plan",
		SHA:         "abc123abc123",
		PR:          100,
		Environment: "production",
		Stacks: []events.StackState{
			{Path: "stacks/a", Status: events.StatusPending},
		},
	}
	if err := UpsertInit(db, initEv); err != nil {
		t.Fatalf("UpsertInit failed: %v", err)
	}

	// Spin up 20 parallel goroutines writing stack status updates
	const workers = 20
	var wg sync.WaitGroup
	errs := make(chan error, workers)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			// Each worker repeatedly updates the stack status to trigger transaction contention
			for j := 0; j < 5; j++ {
				status := events.StatusRunning
				if j%2 == 0 {
					status = events.StatusSafe
				}
				err := UpdateStack(db, execID, "stacks/a", status, fmt.Sprintf("Log from worker %d step %d", workerID, j))
				if err != nil {
					errs <- fmt.Errorf("worker %d step %d failed: %w", workerID, j, err)
					return
				}
				time.Sleep(5 * time.Millisecond) // brief sleep to yield scheduler and create interleaving
			}
		}(i)
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrency error: %v", err)
	}

	// Verify the final state is consistent and can be loaded successfully
	g, err := LoadGraph(db, execID)
	if err != nil {
		t.Fatalf("LoadGraph failed after concurrent updates: %v", err)
	}
	if len(g.Stacks) != 1 || g.Stacks[0].Path != "stacks/a" {
		t.Errorf("loaded graph stacks mismatch: %+v", g.Stacks)
	}
}
