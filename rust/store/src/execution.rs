//! The `executions`, `stacks`, `edges` and `execution_phases` projections of the
//! execution aggregate (Go: `internal/store/execution.go` and
//! `internal/server/shell_exec.go`).

use jiff::Timestamp;
use rusqlite::{Connection, OptionalExtension, Transaction, params};
use tfstackplan_gate::events::{Category, Counts, Edge, Graph, StackState, Status};
use tfstackplan_gate::execution::{self, State};

use crate::{Result, Store, time};

/// The execution-owned columns of an `executions` row.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct ProjectedExecution {
    pub id: String,
    pub repo: String,
    pub sha: String,
    pub pr: i64,
    pub environment: String,
    pub context: String,
    pub log_url: String,
    pub phase: String,
    pub status: String,
    pub progress_label: String,
    pub progress_pct: Option<i64>,
    pub superseded_by: String,
}

/// The execution-owned columns of a `stacks` row.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct ProjectedStack {
    pub path: String,
    pub project: String,
    pub status: Status,
    pub detail: String,
    pub categories: Vec<Category>,
    pub counts: Option<Counts>,
}

/// An `executions` row.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ExecutionRow {
    pub id: String,
    pub repo: String,
    pub sha: String,
    pub pr: i64,
    pub environment: String,
    pub check_run_id: Option<i64>,
    pub rev: i64,
    pub report_markdown: String,
    pub log_url: String,
    /// The execution-level status (`in_progress`, `success`, `failure`).
    pub status: String,
    pub status_context: String,
    pub phase: String,
    pub progress_label: Option<String>,
    pub progress_pct: Option<i64>,
    pub created_at: Timestamp,
    pub superseded_by: String,
    pub change_reasons: String,
}

/// One recorded phase transition.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct PhaseRow {
    pub phase: String,
    pub label: String,
    pub progress_pct: Option<i64>,
    pub at: Timestamp,
}

/// Rewrites the execution's row, stacks and edges from the folded state, and
/// appends a phase history row for each `PhaseChanged` in the batch.
pub(crate) fn project_execution(
    tx: &Transaction<'_>,
    state: &State,
    evs: &[execution::Event],
) -> Result<()> {
    project_execution_row(
        tx,
        &ProjectedExecution {
            id: state.id.clone(),
            repo: state.repo.clone(),
            sha: state.sha.clone(),
            pr: state.pr,
            environment: state.environment.clone(),
            context: state.context.clone(),
            log_url: state.log_url.clone(),
            phase: state.phase.as_str().to_string(),
            status: state.status.clone(),
            progress_label: state.progress_label.clone(),
            progress_pct: state.progress_pct,
            superseded_by: state.superseded_by.clone(),
        },
    )?;
    for s in &state.stacks {
        project_stack(
            tx,
            &state.id,
            &ProjectedStack {
                path: s.path.clone(),
                project: s.project.clone(),
                status: s.run_status,
                detail: s.detail.clone(),
                categories: s.categories.clone(),
                counts: s.counts,
            },
        )?;
    }
    for e in &state.edges {
        project_edge(tx, &state.id, &e.from, &e.to)?;
    }
    for ev in evs {
        if let execution::Event::PhaseChanged(pc) = ev {
            append_phase_history(tx, &state.id, pc.phase.as_str(), &pc.label, pc.pct)?;
        }
    }
    Ok(())
}

/// Upserts the execution-owned columns of an `executions` row, keeping the columns
/// it does not own (`report_markdown`, `change_reasons`, `check_run_id`, `rev`).
/// `superseded_by` is written as folded, and `created_at` resets when a fresh
/// in-progress Init revives a terminal or superseded row.
pub(crate) fn project_execution_row(tx: &Connection, e: &ProjectedExecution) -> Result<()> {
    let label = (!e.progress_label.is_empty()).then_some(e.progress_label.as_str());
    tx.execute(
        "INSERT INTO executions (id, repo, sha, pr, environment, status_context, log_url, phase, status, progress_label, progress_pct, superseded_by)
         VALUES (?1,?2,?3,?4,?5,?6,?7,?8,?9,?10,?11,?12)
         ON CONFLICT(id) DO UPDATE SET
           repo=CASE WHEN excluded.repo='' THEN executions.repo ELSE excluded.repo END,
           sha=CASE WHEN excluded.sha='' THEN executions.sha ELSE excluded.sha END,
           pr=CASE WHEN excluded.pr=0 THEN executions.pr ELSE excluded.pr END,
           environment=CASE WHEN excluded.environment='' THEN executions.environment ELSE excluded.environment END,
           status_context=CASE WHEN excluded.status_context='' THEN executions.status_context ELSE excluded.status_context END,
           log_url=CASE WHEN excluded.log_url='' THEN executions.log_url ELSE excluded.log_url END,
           phase=CASE WHEN excluded.phase='' THEN executions.phase ELSE excluded.phase END,
           status=CASE WHEN excluded.status='' THEN executions.status ELSE excluded.status END,
           progress_label=CASE WHEN excluded.progress_label IS NULL THEN executions.progress_label ELSE excluded.progress_label END,
           progress_pct=CASE WHEN excluded.progress_pct IS NULL THEN executions.progress_pct ELSE excluded.progress_pct END,
           superseded_by=excluded.superseded_by,
           created_at=CASE WHEN excluded.status='in_progress'
                            AND (executions.status != 'in_progress'
                                 OR (COALESCE(executions.superseded_by,'') != '' AND COALESCE(excluded.superseded_by,'') = ''))
                           THEN CURRENT_TIMESTAMP ELSE executions.created_at END",
        params![
            e.id,
            e.repo,
            e.sha,
            e.pr,
            e.environment,
            e.context,
            e.log_url,
            e.phase,
            e.status,
            label,
            e.progress_pct,
            e.superseded_by
        ],
    )?;
    Ok(())
}

/// Upserts the execution-owned columns of a `stacks` row. Categories and counts
/// are JSON, and an empty value keeps the stored one.
pub(crate) fn project_stack(tx: &Connection, exec_id: &str, s: &ProjectedStack) -> Result<()> {
    let cats = if s.categories.is_empty() {
        String::new()
    } else {
        serde_json::to_string(&s.categories)?
    };
    let counts = match &s.counts {
        Some(c) => serde_json::to_string(c)?,
        None => String::new(),
    };
    tx.execute(
        "INSERT INTO stacks (execution_id, stack_path, project, status, detail, categories, counts)
         VALUES (?1,?2,?3,?4,?5,?6,?7)
         ON CONFLICT(execution_id, stack_path) DO UPDATE SET
           project=CASE WHEN excluded.project='' THEN stacks.project ELSE excluded.project END,
           status=excluded.status, detail=excluded.detail,
           categories=CASE WHEN excluded.categories='' THEN stacks.categories ELSE excluded.categories END,
           counts=CASE WHEN excluded.counts='' THEN stacks.counts ELSE excluded.counts END",
        params![
            exec_id,
            s.path,
            s.project,
            s.status.as_str(),
            s.detail,
            cats,
            counts
        ],
    )?;
    Ok(())
}

/// Inserts a dependency edge, ignoring duplicates.
pub(crate) fn project_edge(tx: &Connection, exec_id: &str, from: &str, to: &str) -> Result<()> {
    tx.execute(
        "INSERT OR IGNORE INTO edges (execution_id, from_stack, to_stack) VALUES (?1,?2,?3)",
        params![exec_id, from, to],
    )?;
    Ok(())
}

/// Appends an immutable `execution_phases` row.
pub(crate) fn append_phase_history(
    tx: &Connection,
    exec_id: &str,
    phase: &str,
    label: &str,
    pct: Option<i64>,
) -> Result<()> {
    let label = (!label.is_empty()).then_some(label);
    tx.execute(
        "INSERT INTO execution_phases (execution_id, phase, label, progress_pct) VALUES (?1,?2,?3,?4)",
        params![exec_id, phase, label, pct],
    )?;
    Ok(())
}

/// The newest execution of (pr, environment).
pub(crate) fn latest_execution_id_in(
    conn: &Connection,
    pr: i64,
    environment: &str,
) -> Result<Option<String>> {
    Ok(conn
        .query_row(
            "SELECT id FROM executions WHERE pr = ?1 AND environment = ?2
             ORDER BY created_at DESC, id DESC LIMIT 1",
            params![pr, environment],
            |r| r.get(0),
        )
        .optional()?)
}

impl Store {
    /// The newest execution id of (pr, environment), if any.
    pub fn latest_execution_id(&self, pr: i64, environment: &str) -> Result<Option<String>> {
        latest_execution_id_in(&self.conn(), pr, environment)
    }

    /// One `executions` row.
    pub fn get_execution(&self, id: &str) -> Result<Option<ExecutionRow>> {
        let conn = self.conn();
        let row = conn
            .query_row(
                "SELECT id, repo, sha, COALESCE(pr,0), COALESCE(environment,''), check_run_id,
                        rev, COALESCE(report_markdown,''), COALESCE(log_url,''),
                        COALESCE(status,''), COALESCE(status_context,''), COALESCE(phase,''),
                        progress_label, progress_pct, created_at, COALESCE(superseded_by, ''),
                        COALESCE(change_reasons, '')
                 FROM executions WHERE id = ?1",
                [id],
                |r| {
                    Ok((
                        ExecutionRow {
                            id: r.get(0)?,
                            repo: r.get(1)?,
                            sha: r.get(2)?,
                            pr: r.get(3)?,
                            environment: r.get(4)?,
                            check_run_id: r.get(5)?,
                            rev: r.get(6)?,
                            report_markdown: r.get(7)?,
                            log_url: r.get(8)?,
                            status: r.get(9)?,
                            status_context: r.get(10)?,
                            phase: r.get(11)?,
                            progress_label: r.get(12)?,
                            progress_pct: r.get(13)?,
                            created_at: Timestamp::UNIX_EPOCH,
                            superseded_by: r.get(15)?,
                            change_reasons: r.get(16)?,
                        },
                        r.get::<_, String>(14)?,
                    ))
                },
            )
            .optional()?;
        row.map(|(mut e, created_at)| {
            e.created_at = time::from_sql(&created_at)?;
            Ok(e)
        })
        .transpose()
    }

    /// The stacks (by path) and edges of an execution. Unparseable categories or
    /// counts read as absent, as in Go.
    pub fn load_graph(&self, id: &str) -> Result<Graph> {
        let conn = self.conn();
        let mut stmt = conn.prepare(
            "SELECT stack_path, COALESCE(project,''), COALESCE(status,''), COALESCE(detail,''), COALESCE(categories,''), COALESCE(counts,'')
             FROM stacks WHERE execution_id = ?1 ORDER BY stack_path",
        )?;
        let rows = stmt
            .query_map([id], |r| {
                Ok((
                    r.get::<_, String>(0)?,
                    r.get::<_, String>(1)?,
                    r.get::<_, String>(2)?,
                    r.get::<_, String>(3)?,
                    r.get::<_, String>(4)?,
                    r.get::<_, String>(5)?,
                ))
            })?
            .collect::<rusqlite::Result<Vec<_>>>()?;
        let stacks = rows
            .into_iter()
            .map(|(path, project, status, detail, cats, counts)| {
                Ok(StackState {
                    path,
                    project,
                    status: Status::parse(&status)?,
                    detail,
                    categories: serde_json::from_str(&cats).unwrap_or_default(),
                    counts: serde_json::from_str(&counts).ok(),
                })
            })
            .collect::<Result<Vec<_>>>()?;
        let mut stmt = conn.prepare(
            "SELECT from_stack, to_stack FROM edges WHERE execution_id = ?1 ORDER BY from_stack, to_stack",
        )?;
        let edges = stmt
            .query_map([id], |r| {
                Ok(Edge {
                    from: r.get(0)?,
                    to: r.get(1)?,
                })
            })?
            .collect::<rusqlite::Result<Vec<_>>>()?;
        Ok(Graph { stacks, edges })
    }

    /// An execution's phase transitions, oldest first.
    pub fn phases_for(&self, execution_id: &str) -> Result<Vec<PhaseRow>> {
        let conn = self.conn();
        let mut stmt = conn.prepare(
            "SELECT phase, COALESCE(label,''), progress_pct, at FROM execution_phases
             WHERE execution_id = ?1 ORDER BY at, rowid",
        )?;
        let rows = stmt
            .query_map([execution_id], |r| {
                Ok((
                    r.get::<_, String>(0)?,
                    r.get::<_, String>(1)?,
                    r.get::<_, Option<i64>>(2)?,
                    r.get::<_, String>(3)?,
                ))
            })?
            .collect::<rusqlite::Result<Vec<_>>>()?;
        rows.into_iter()
            .map(|(phase, label, progress_pct, at)| {
                Ok(PhaseRow {
                    phase,
                    label,
                    progress_pct,
                    at: time::from_sql(&at)?,
                })
            })
            .collect()
    }
}

#[cfg(test)]
mod tests;
