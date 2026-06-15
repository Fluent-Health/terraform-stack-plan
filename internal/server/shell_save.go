package server

import (
	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/reconcile"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// save persists a ChangeSet's gate state to gate_targets in one transaction:
// it marks classified (for any non-NotClassified gate), upserts the desired
// targets with their grant state + requester, and deletes any persisted target
// the new state no longer carries (the prune from gap ②). Execution/stack rows
// are written by the existing runner-event path.
//
// Unlike the legacy UpsertTarget, this writes `requester` in the upsert because
// the lease lives inside the gate state and is always carried forward by Step —
// so the old ON CONFLICT carve-out (and the clobber bug) is gone.
func (sh *Shell) save(cs reconcile.ChangeSet) error {
	tx, err := sh.app.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, isNC := cs.Gate.(reconcile.NotClassified); !isNC {
		if _, err := tx.Exec(
			`INSERT INTO gate_runs (pr, environment) VALUES (?, ?)
			 ON CONFLICT(pr, environment) DO UPDATE SET classified_at = CURRENT_TIMESTAMP`,
			cs.PR, cs.Environment); err != nil {
			return err
		}
	}

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
