package store

import (
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

func TestStoreConcurrency(t *testing.T) {
	db := newTestDB(t)

	// Initialize an execution with a single stack (mirrors shell.projectExecution's
	// write pattern: ProjectExecutionRow + ProjectStack are the sole write path
	// now — UpsertInit/UpdateStack were the legacy direct-write authority).
	execID := "concurrent-exec-1"
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := ProjectExecutionRow(tx, ProjectedExecution{
		ID: execID, Repo: "Fluent-Health/terraform-stack-plan", SHA: "abc123abc123",
		PR: 100, Environment: "production", Status: "in_progress",
	}); err != nil {
		t.Fatal(err)
	}
	if err := ProjectStack(tx, execID, ProjectedStack{Path: "stacks/a", Status: events.StatusPending}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
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
				detail := fmt.Sprintf("Log from worker %d step %d", workerID, j)
				err := Transact(db, func(tx *sql.Tx) error {
					return ProjectStack(tx, execID, ProjectedStack{Path: "stacks/a", Status: status, Detail: detail})
				})
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
