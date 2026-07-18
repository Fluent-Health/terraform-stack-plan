package server

import (
	"context"

	"github.com/Fluent-Health/terraform-stack-plan/internal/execution"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// HandleExec processes one execution-lifecycle signal for execID: gather (replay)
// → Decide → fold (Evolve) → persist (append + snapshot) → project. Serialized per
// execID. Lock ordering is strictly one-directional — gate-before-exec, never the
// reverse: the run-trigger path holds the (pr,env) gate lock across
// execute→materializeRun→HandleExec (so gate is the outer lock there), while the
// ingest handlers take the exec lock alone. No code ever acquires the gate lock
// while holding the exec lock, so the two aggregates cannot deadlock. (projectExecution
// takes no lock.) Preserve this ordering when adding new cross-aggregate paths.
func (sh *Shell) HandleExec(ctx context.Context, execID string, sig execution.Signal) error {
	m := sh.execLockFor(execID)
	m.Lock()
	defer m.Unlock()

	stream := runStreamID(execID)
	state, version, err := sh.app.execDecider.Load(sh.app.eventStore, stream)
	if err != nil {
		return err
	}
	evs := execution.Decide(state, sig)
	if len(evs) == 0 {
		return nil
	}
	for _, e := range evs {
		state = execution.Evolve(state, e)
	}
	if err := sh.app.execDecider.Append(sh.app.eventStore, stream, version, evs, state); err != nil {
		return err
	}
	return sh.projectExecution(state, evs)
}

// projectExecution rebuilds the execution-aggregate-owned columns of the
// executions/stacks/edges rows from the folded state, and appends execution_phases
// history for each PhaseChanged in the batch. Owned columns only — never touches
// report_markdown/change_reasons/superseded_by/check_run_id/created_at.
func (sh *Shell) projectExecution(state execution.State, evs []execution.Event) error {
	tx, err := sh.app.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	pl := state.ProgressLabel
	if err := store.ProjectExecutionRow(tx, store.ProjectedExecution{
		ID: state.ID, Repo: state.Repo, SHA: state.SHA, PR: state.PR,
		Environment: state.Environment, Context: state.Context, LogURL: state.LogURL,
		Phase: string(state.Phase), Status: state.Status,
		ProgressLabel: pl, ProgressPct: state.ProgressPct,
	}); err != nil {
		return err
	}
	for _, s := range state.Stacks {
		if err := store.ProjectStack(tx, state.ID, store.ProjectedStack{
			Path: s.Path, Project: s.Project, Status: s.RunStatus,
			Detail: s.Detail, Categories: s.Categories, Counts: s.Counts,
		}); err != nil {
			return err
		}
	}
	for _, e := range state.Edges {
		if err := store.ProjectEdge(tx, state.ID, e.From, e.To); err != nil {
			return err
		}
	}
	for _, ev := range evs {
		if pc, ok := ev.(execution.PhaseChanged); ok {
			if err := store.AppendPhaseHistory(tx, state.ID, string(pc.Phase), pc.Label, pc.Pct); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
