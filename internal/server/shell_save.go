package server

import (
	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/reconcile"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// persist appends the events Decide produced to the (pr,env) event stream, writes
// a snapshot of the folded state, then refreshes the gate_targets projection. The
// event log is the source of truth; gate_targets is a derived index. Append and
// the projection are not one transaction — safe, because the projection is
// rebuildable from the log and self-heals on the next gather.
func (sh *Shell) persist(pr int, env string, expectedVersion int, evs []reconcile.Event, state reconcile.ChangeSet) error {
	if err := sh.app.gateDecider.Append(sh.app.eventStore, execStreamID(pr, env), expectedVersion, evs, state); err != nil {
		return err
	}
	return sh.project(state, evs)
}

// project writes the gate_targets index + stacks.status overlay derived from the
// folded gate state. gate_targets is a derived projection of the event log;
// gate_runs has been retired (migration 008).
func (sh *Shell) project(cs reconcile.ChangeSet, evs []reconcile.Event) error {
	tx, err := sh.app.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	desired := reconcileGateTargets(cs.Gate)
	keep := map[string]bool{}
	requester := reconcile.Requester(cs.Gate)
	for _, t := range desired {
		keep[t.Class+"|"+t.Target] = true
		if _, err := tx.Exec(
			`INSERT INTO gate_targets (pr, environment, class, target, grant_name, state, requester)
			 VALUES (?,?,?,?,?,?,?)
			 ON CONFLICT(pr, environment, class, target) DO UPDATE SET
			   grant_name=excluded.grant_name, state=excluded.state,
			   requester=excluded.requester, updated_at=CURRENT_TIMESTAMP`,
			cs.PR, cs.Environment, t.Class, t.Target, t.GrantName, string(t.Grant), requester); err != nil {
			return err
		}
	}

	// Run-lifecycle projection: a RunStartFailed IN THIS BATCH marks its
	// execution row failed — the runner will never report, so nothing else
	// would move the row off in_progress (the terminal check render derives
	// from this). Scoped to the event batch, not the folded state: a stale
	// start_failed entry must not keep flipping a row a real runner later
	// revived (a client-side start timeout whose build actually ran).
	for _, e := range evs {
		if rf, ok := e.(reconcile.RunStartFailed); ok {
			if _, err := tx.Exec(
				`UPDATE executions SET status = 'failure' WHERE id = ? AND status = 'in_progress'`,
				rf.ExecutionID); err != nil {
				return err
			}
		}
	}

	// Prune persisted targets the new state dropped.
	// Prune set is read via the pooled connection (not tx): under the per-(pr,env)
	// lock no other writer touches these rows, and the tx's own upserts are in
	// `keep`, so this can only select genuinely-dropped targets for deletion.
	existing, err := store.TargetsFor(sh.app.db, cs.PR, cs.Environment)
	if err != nil {
		return err
	}
	for _, e := range existing {
		if !keep[e.Class+"|"+e.Target] {
			if err := store.DeleteTarget(tx, cs.PR, cs.Environment, e.Class, e.Target); err != nil {
				return err
			}
		}
	}
	// Write the derived gated/safe overlay onto the changeset's stacks (the live
	// page reads stacks.status). Mirrors the legacy gated-flip + gated→safe flip,
	// now owned by the gate state. failed/moving always win, so skip them.
	if display := overlayStatus(cs.Gate); display != "" {
		if execID, ok := store.LatestExecutionID(sh.app.db, cs.PR, cs.Environment); ok {
			for _, t := range desired {
				if _, err := tx.Exec(
					`UPDATE stacks SET status = ?
					   WHERE execution_id = ? AND project = ?
					     AND status NOT IN (?, ?)`,
					display, execID, t.Target,
					string(events.StatusFailed), string(events.StatusMoving)); err != nil {
					return err
				}
			}
		}
	}

	return tx.Commit()
}

// reconcileGateTargets reads the targets carried by any stateful gate variant.
func reconcileGateTargets(g reconcile.GateState) []reconcile.Target {
	switch v := g.(type) {
	case reconcile.Pending:
		return v.Targets
	case reconcile.Satisfied:
		return v.Targets
	case reconcile.Blocked:
		return v.Targets
	}
	return nil
}

// overlayStatus is the derived gated/safe status a gate imposes on its stacks.
// "" means no overlay (NotClassified/Clean impose nothing — clean stacks keep
// their runner-told status).
func overlayStatus(g reconcile.GateState) string {
	switch g.(type) {
	case reconcile.Satisfied:
		return string(events.StatusSafe)
	case reconcile.Pending, reconcile.Blocked:
		return string(events.StatusGated)
	}
	return ""
}
