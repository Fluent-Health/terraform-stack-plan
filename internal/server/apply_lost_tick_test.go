package server

import (
	"context"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// lostTickApply replays an apply that hung in `report` up to its terminal finalize:
// three stacks start, only two terminal ticks arrive (the third stack's /api/update
// 500'd), then the runner reports PhaseReport and sends fin. It returns the
// folded graph, the execution, and the last conclusion posted to the check run.
func lostTickApply(t *testing.T, fin events.Finalize) (events.Graph, store.Execution, string) {
	t.Helper()
	db := newServerTestDB(t)
	var mu sync.Mutex
	conclusion := ""
	gh := &MockGitHub{
		CreateCheckRunFn: func(context.Context, string, string, string, string) (int64, error) { return 42, nil },
		UpdateCheckRunFn: func(_ context.Context, _ string, _ int64, u CheckRunUpdate) error {
			mu.Lock()
			defer mu.Unlock()
			if u.Conclusion != "" {
				conclusion = u.Conclusion
			}
			return nil
		},
	}
	a := New(db, gh, Config{})
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	const id = "run-7-staging-apply-abc123abc123-a1"
	stacks := []string{"stacks/a", "stacks/b", "stacks/c"}
	init := events.Init{ID: id, Repo: "o/r", SHA: "abc123abc123", PR: 7, Environment: "staging", Context: "apply/staging"}
	for _, s := range stacks {
		init.Stacks = append(init.Stacks, events.StackState{Path: s, Status: events.StatusPending})
	}
	post(t, srv, "/api/init", init)
	// The mid-apply classify Finalize (non-failed, before PhaseApplying) must not conclude anything.
	post(t, srv, "/api/finalize", events.Finalize{ID: id, ReportMarkdown: "classify pass"})
	post(t, srv, "/api/phase", events.PhaseEvent{ID: id, Phase: events.PhaseApplying})
	for _, s := range stacks {
		post(t, srv, "/api/update", events.Update{ID: id, Stack: s, Status: events.StatusRunning})
	}
	if g, _ := store.LoadGraph(db, id); countStatus(g, events.StatusRunning) != 3 {
		t.Fatalf("classify finalize concluded stacks early: %+v", g.Stacks)
	}
	post(t, srv, "/api/update", events.Update{ID: id, Stack: stacks[0], Status: events.StatusSafe})
	post(t, srv, "/api/update", events.Update{ID: id, Stack: stacks[1], Status: events.StatusSafe})
	// stacks[2]'s terminal tick is lost.
	post(t, srv, "/api/phase", events.PhaseEvent{ID: id, Phase: events.PhaseReport})
	post(t, srv, "/api/finalize", fin)

	g, err := store.LoadGraph(db, id)
	if err != nil {
		t.Fatal(err)
	}
	e, err := store.GetExecution(db, id)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	return g, e, conclusion
}

func countStatus(g events.Graph, st events.Status) int {
	n := 0
	for _, s := range g.Stacks {
		if s.Status == st {
			n++
		}
	}
	return n
}

func TestSuccessfulApplyFinalizeConcludesLostTick(t *testing.T) {
	g, e, conclusion := lostTickApply(t, events.Finalize{ID: "run-7-staging-apply-abc123abc123-a1"})
	if n := countStatus(g, events.StatusSafe); n != 3 {
		t.Errorf("safe stacks = %d, want 3: %+v", n, g.Stacks)
	}
	if e.Status != "success" {
		t.Errorf("execution status = %q, want success", e.Status)
	}
	if conclusion != "success" {
		t.Errorf("check run conclusion = %q, want success", conclusion)
	}
}

func TestFailedApplyFinalizeDoesNotConcludeLostTick(t *testing.T) {
	g, e, conclusion := lostTickApply(t, events.Finalize{ID: "run-7-staging-apply-abc123abc123-a1", Failed: true})
	if n := countStatus(g, events.StatusSafe); n != 2 {
		t.Errorf("safe stacks = %d, want 2 (a failed apply must not mark the unreported stack done): %+v", n, g.Stacks)
	}
	if n := countStatus(g, events.StatusAborted); n != 1 {
		t.Errorf("aborted stacks = %d, want 1: %+v", n, g.Stacks)
	}
	if e.Status != "failure" {
		t.Errorf("execution status = %q, want failure", e.Status)
	}
	if conclusion != "failure" {
		t.Errorf("check run conclusion = %q, want failure", conclusion)
	}
}
