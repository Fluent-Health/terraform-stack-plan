package server

import (
	"database/sql"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// snapshot is the DB-derived input to the verdict state machine: everything the
// projections need and nothing else (no db, no clock), so they are pure.
type snapshot struct {
	phase         events.Phase
	totalStacks   int
	plannedStacks int  // stacks past pending/running (done planning)
	anyFailed     bool // any stack failed
	finalized     bool // finalize has run (report stored)
	totalGates    int  // distinct (class,target) gates recorded
	activeGates   int  // gates whose grant is ACTIVE
}

// loadSnapshot derives the verdict input for an execution purely from the DB.
// ok is false when the execution is unknown.
func loadSnapshot(db *sql.DB, id string) (snapshot, store.Execution, bool) {
	exec, err := store.GetExecution(db, id)
	if err != nil {
		return snapshot{}, store.Execution{}, false
	}
	var snap snapshot
	snap.phase = events.Phase(exec.Phase)
	// SetReport stores the report first thing in finalize, so a non-empty report
	// is the reliable "classified / done planning" signal.
	snap.finalized = exec.ReportMarkdown != ""

	g, err := store.LoadGraph(db, id)
	if err == nil {
		for _, s := range g.Stacks {
			snap.totalStacks++
			if s.Status != events.StatusPending && s.Status != events.StatusRunning {
				snap.plannedStacks++
			}
			if s.Status == events.StatusFailed {
				snap.anyFailed = true
			}
		}
	}

	if exec.PR != 0 {
		if targets, terr := store.TargetsFor(db, exec.PR, exec.Environment); terr == nil {
			snap.totalGates = len(targets)
			for _, t := range targets {
				if t.State == "ACTIVE" {
					snap.activeGates++
				}
			}
		}
	}
	return snap, exec, true
}

// conclusion projects DB state onto a GitHub check-run conclusion. "" means
// leave the run in_progress (still planning). action_required is used for an
// unsatisfied gate so a required check blocks the merge until approval flips it.
func conclusion(s snapshot) string {
	switch {
	case s.anyFailed:
		return "failure"
	case s.totalGates > 0:
		if s.activeGates >= s.totalGates {
			return "success"
		}
		return "action_required"
	case !s.finalized:
		return ""
	default:
		return "success"
	}
}
