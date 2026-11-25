//! The `gate_targets` projection (Go: `internal/store/gate.go` and the gate half
//! of `internal/server/shell_save.go`). It is rebuilt from the folded gate stream
//! and is a cross-PR index for the reconcile loop, the orphan sweep and the
//! approvals view, never the verdict's truth.

use std::collections::HashSet;

use rusqlite::{Transaction, params};
use tfstackplan_gate::events::Status;
use tfstackplan_gate::reconcile::{self, ChangeSet, Event, GateState};

use crate::execution::latest_execution_id_in;
use crate::{Result, Store};

/// One `gate_targets` row: a (class, target) grant recorded for a (PR, environment).
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct GateTargetRow {
    pub class: String,
    pub target: String,
    pub grant_name: String,
    pub state: String,
    pub requester: String,
}

/// A (PR, environment) whose verdict may still need posting.
#[derive(Debug, Clone, Default, PartialEq, Eq, PartialOrd, Ord)]
pub struct GateKey {
    pub pr: i64,
    pub environment: String,
}

/// A PR still holding an open grant, with the repo of its latest execution (empty
/// when it has none).
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct OpenGrantPr {
    pub pr: i64,
    pub repo: String,
}

/// A gate target awaiting human action, with its repo.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct PendingApproval {
    pub pr: i64,
    pub environment: String,
    pub repo: String,
    pub class: String,
    pub target: String,
    pub grant_name: String,
    pub state: String,
    pub requester: String,
}

/// An (environment, class, target) recorded for a PR.
#[derive(Debug, Clone, Default, PartialEq, Eq, PartialOrd, Ord)]
pub struct PrTarget {
    pub environment: String,
    pub class: String,
    pub target: String,
}

/// Rewrites the projections derived from a folded gate: the `gate_targets` rows of
/// its (PR, environment), the failed status of an execution whose start failed in
/// this batch, and the gated/safe overlay on the latest execution's stacks.
pub(crate) fn project_gate(tx: &Transaction<'_>, cs: &ChangeSet, evs: &[Event]) -> Result<()> {
    let desired = cs.gate.targets().unwrap_or_default();
    let requester = reconcile::requester(&cs.gate);
    let mut keep = HashSet::new();
    for t in desired {
        keep.insert((t.class.as_str(), t.target.as_str()));
        tx.execute(
            "INSERT INTO gate_targets (pr, environment, class, target, grant_name, state, requester)
             VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7)
             ON CONFLICT(pr, environment, class, target) DO UPDATE SET
               grant_name=excluded.grant_name, state=excluded.state,
               requester=excluded.requester, updated_at=CURRENT_TIMESTAMP",
            params![
                cs.pr,
                cs.environment,
                t.class,
                t.target,
                t.grant_name,
                t.grant.as_str(),
                requester
            ],
        )?;
    }

    // A RunStartFailed in this batch marks its execution failed: the runner will
    // never report on it. Scoped to the batch, so a stale start failure never flips
    // a row a real runner later revived.
    for e in evs {
        if let Event::RunStartFailed(rf) = e {
            tx.execute(
                "UPDATE executions SET status = 'failure' WHERE id = ?1 AND status = 'in_progress'",
                [&rf.execution_id],
            )?;
        }
    }

    // Prune the targets the fold dropped.
    for t in targets_for_in(tx, cs.pr, &cs.environment)? {
        if !keep.contains(&(t.class.as_str(), t.target.as_str())) {
            delete_target(tx, cs.pr, &cs.environment, &t.class, &t.target)?;
        }
    }

    // The gated/safe overlay on the latest execution's gate-target stacks. Failed
    // and aborted are terminal and always win; moving does not.
    if let Some(display) = overlay_status(&cs.gate)
        && let Some(exec_id) = latest_execution_id_in(tx, cs.pr, &cs.environment)?
    {
        for t in desired {
            tx.execute(
                "UPDATE stacks SET status = ?1
                   WHERE execution_id = ?2 AND project = ?3 AND status NOT IN (?4, ?5)",
                params![
                    display.as_str(),
                    exec_id,
                    t.target,
                    Status::Failed.as_str(),
                    Status::Aborted.as_str()
                ],
            )?;
        }
    }
    Ok(())
}

/// Removes one gate target row: the prune half of the projection.
pub(crate) fn delete_target(
    tx: &Transaction<'_>,
    pr: i64,
    environment: &str,
    class: &str,
    target: &str,
) -> Result<()> {
    tx.execute(
        "DELETE FROM gate_targets WHERE pr=?1 AND environment=?2 AND class=?3 AND target=?4",
        params![pr, environment, class, target],
    )?;
    Ok(())
}

/// The status a gate imposes on its stacks; none for `NotClassified` and `Clean`.
fn overlay_status(g: &GateState) -> Option<Status> {
    match g {
        GateState::Satisfied(_) => Some(Status::Safe),
        GateState::Pending(_) | GateState::Blocked(_) => Some(Status::Gated),
        GateState::NotClassified | GateState::Clean => None,
    }
}

fn targets_for_in(
    conn: &rusqlite::Connection,
    pr: i64,
    environment: &str,
) -> Result<Vec<GateTargetRow>> {
    let mut stmt = conn.prepare_cached(
        "SELECT class, target, COALESCE(grant_name,''), COALESCE(state,''), COALESCE(requester,'')
         FROM gate_targets WHERE pr = ?1 AND environment = ?2",
    )?;
    let rows = stmt
        .query_map(params![pr, environment], |r| {
            Ok(GateTargetRow {
                class: r.get(0)?,
                target: r.get(1)?,
                grant_name: r.get(2)?,
                state: r.get(3)?,
                requester: r.get(4)?,
            })
        })?
        .collect::<rusqlite::Result<Vec<_>>>()?;
    Ok(rows)
}

impl Store {
    /// The (PR, environment) gates with at least one target not yet active: the
    /// reconcile loop's work list.
    pub fn pending_gates(&self) -> Result<Vec<GateKey>> {
        let conn = self.conn();
        let mut stmt = conn.prepare(
            "SELECT pr, environment FROM gate_targets
             GROUP BY pr, environment
             HAVING SUM(CASE WHEN state = 'ACTIVE' THEN 1 ELSE 0 END) < COUNT(*)",
        )?;
        let rows = stmt
            .query_map([], |r| {
                Ok(GateKey {
                    pr: r.get(0)?,
                    environment: r.get(1)?,
                })
            })?
            .collect::<rusqlite::Result<Vec<_>>>()?;
        Ok(rows)
    }

    /// Every gate target recorded for (pr, environment).
    pub fn targets_for(&self, pr: i64, environment: &str) -> Result<Vec<GateTargetRow>> {
        targets_for_in(&self.conn(), pr, environment)
    }

    /// Every PR with a target in an open state (AWAITING, ACTIVATING, ACTIVE), once
    /// per PR, with its latest execution's repo: the orphan sweep's work list.
    pub fn open_grant_prs(&self) -> Result<Vec<OpenGrantPr>> {
        let conn = self.conn();
        let mut stmt = conn.prepare(
            "SELECT gt.pr,
                    COALESCE((SELECT e.repo FROM executions e
                              WHERE e.pr = gt.pr ORDER BY e.created_at DESC, e.id DESC LIMIT 1), '')
             FROM gate_targets gt
             WHERE gt.state IN ('AWAITING','ACTIVATING','ACTIVE')
             GROUP BY gt.pr
             ORDER BY gt.pr",
        )?;
        let rows = stmt
            .query_map([], |r| {
                Ok(OpenGrantPr {
                    pr: r.get(0)?,
                    repo: r.get(1)?,
                })
            })?
            .collect::<rusqlite::Result<Vec<_>>>()?;
        Ok(rows)
    }

    /// Every gate target not yet active, across PRs and environments: the approvals
    /// to-do list.
    pub fn pending_approvals(&self) -> Result<Vec<PendingApproval>> {
        let conn = self.conn();
        let mut stmt = conn.prepare(
            "SELECT gt.pr, gt.environment,
                    COALESCE((SELECT e.repo FROM executions e
                              WHERE e.pr = gt.pr AND e.environment = gt.environment
                              ORDER BY e.created_at DESC, e.id DESC LIMIT 1), ''),
                    gt.class, gt.target, COALESCE(gt.grant_name,''), COALESCE(gt.state,''),
                    COALESCE(gt.requester,'')
             FROM gate_targets gt
             WHERE gt.state != 'ACTIVE'
             ORDER BY gt.pr, gt.environment, gt.class, gt.target",
        )?;
        let rows = stmt
            .query_map([], |r| {
                Ok(PendingApproval {
                    pr: r.get(0)?,
                    environment: r.get(1)?,
                    repo: r.get(2)?,
                    class: r.get(3)?,
                    target: r.get(4)?,
                    grant_name: r.get(5)?,
                    state: r.get(6)?,
                    requester: r.get(7)?,
                })
            })?
            .collect::<rusqlite::Result<Vec<_>>>()?;
        Ok(rows)
    }

    /// Every (environment, class, target) recorded for `pr`.
    pub fn pr_targets(&self, pr: i64) -> Result<Vec<PrTarget>> {
        let conn = self.conn();
        let mut stmt = conn.prepare(
            "SELECT DISTINCT environment, class, target FROM gate_targets WHERE pr = ?1",
        )?;
        let rows = stmt
            .query_map([pr], |r| {
                Ok(PrTarget {
                    environment: r.get(0)?,
                    class: r.get(1)?,
                    target: r.get(2)?,
                })
            })?
            .collect::<rusqlite::Result<Vec<_>>>()?;
        Ok(rows)
    }
}

#[cfg(test)]
mod tests;
