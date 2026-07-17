package server

import (
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/execution"
)

func TestExecDeciderRoundTripsThroughStore(t *testing.T) {
	app := newTestShell(t).app
	stream := runStreamID("run-1-nonprod-plan-abc-a1")

	// Append: init then a stack tick, folding via the aggregate as the shell will.
	state := execution.Empty()
	var version int
	for _, sig := range []execution.Signal{
		execution.ReportInit{Exec: execution.State{ID: "run-1-nonprod-plan-abc-a1", Stacks: []execution.Stack{{Path: "a"}}}},
		execution.ReportTick{Stack: "a", Status: events.StatusRunning},
	} {
		evs := execution.Decide(state, sig)
		newState := state
		for _, e := range evs {
			newState = execution.Evolve(newState, e)
		}
		if err := app.execDecider.Append(app.eventStore, stream, version, evs, newState); err != nil {
			t.Fatalf("append: %v", err)
		}
		version += len(evs)
		state = newState
	}

	// Load: replay reconstructs the same folded state.
	got, gotVer, err := app.execDecider.Load(app.eventStore, stream)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if gotVer != 2 {
		t.Fatalf("want version 2, got %d", gotVer)
	}
	if got.ID != "run-1-nonprod-plan-abc-a1" || len(got.Stacks) != 1 || got.Stacks[0].RunStatus != events.StatusRunning {
		t.Fatalf("replayed state mismatch: %#v", got)
	}
}
