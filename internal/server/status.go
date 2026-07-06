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

// conclusion projects DB state + the merge-lock verdict onto a GitHub check-run
// conclusion. "" means leave the run in_progress. Precedence: a failed plan >
// an unsatisfied gate (action_required) > still planning > merge-lock blocked
// (in_progress until the overlapping apply releases) > success. The zero-value
// lock verdict means "not evaluated" (legacy two-check mode) and never blocks.
func conclusion(s snapshot, lock applyLockVerdict) string {
	switch {
	case s.anyFailed:
		return "failure"
	case s.totalGates > 0 && s.activeGates < s.totalGates:
		return "action_required"
	case !s.finalized:
		return ""
	case lockBlocked(lock):
		return ""
	default:
		return "success"
	}
}
