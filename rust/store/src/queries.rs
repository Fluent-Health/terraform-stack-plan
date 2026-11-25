//! The tables and queries only the HTTP handlers use (Go: `internal/store/prmeta.go`,
//! `output.go`, `applylock_checks.go` and the list/lookup half of `execution.go`).
//! None of these are projections of an event stream: `pr_meta`, `stack_outputs` and
//! `applylock_checks` are written directly by the handlers, and `report_markdown`,
//! `change_reasons`, `rev` and `check_run_id` are the `executions` columns the
//! execution aggregate does not own.

use jiff::Timestamp;
use rusqlite::{Connection, OptionalExtension, Row, params};
use tfstackplan_gate::events::ChangeReason;

use crate::{Result, Store, time};

/// A `pr_meta` row: a PR's identity as the GitHub webhook last reported it.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct PrMeta {
    pub repo: String,
    pub pr: i64,
    pub title: String,
    pub body: String,
    pub author_login: String,
    pub head_ref: String,
    pub url: String,
    pub auto_merge: bool,
    /// Set by the database; ignored on upsert.
    pub updated_at: Option<Timestamp>,
}

/// A persisted apply-lock check run, keyed by (environment, head SHA), so a later
/// claim release can patch it.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct ApplyLockCheck {
    pub environment: String,
    pub head_sha: String,
    pub check_run_id: i64,
    pub pr: i64,
    pub repo: String,
    /// `None` is Go's nil slice, stored as `null`; `Some(vec![])` is stored as `[]`.
    pub stacks: Option<Vec<String>>,
    /// clear, held or unverifiable.
    pub state: String,
    /// merge_group or pr_head.
    pub kind: String,
    /// The plan execution whose check carries the consolidated render; empty for
    /// legacy and merge-group records.
    pub execution_id: String,
}

/// The summary columns of an `executions` row, as the list queries return them
/// (no report markdown).
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ExecutionSummary {
    pub id: String,
    pub repo: String,
    pub sha: String,
    pub pr: i64,
    pub environment: String,
    pub status: String,
    pub status_context: String,
    pub phase: String,
    pub progress_label: Option<String>,
    pub progress_pct: Option<i64>,
    pub created_at: Timestamp,
    pub superseded_by: String,
    pub log_url: String,
}

const LIST_EXECUTION_COLUMNS: &str = "id, repo, COALESCE(sha,''), COALESCE(pr,0), COALESCE(environment,''),
    COALESCE(status,''), COALESCE(status_context,''), COALESCE(phase,''), progress_label, progress_pct, created_at,
    COALESCE(superseded_by,''), COALESCE(log_url,'')";

fn summary_row(r: &Row<'_>) -> rusqlite::Result<(ExecutionSummary, String)> {
    Ok((
        ExecutionSummary {
            id: r.get(0)?,
            repo: r.get(1)?,
            sha: r.get(2)?,
            pr: r.get(3)?,
            environment: r.get(4)?,
            status: r.get(5)?,
            status_context: r.get(6)?,
            phase: r.get(7)?,
            progress_label: r.get(8)?,
            progress_pct: r.get(9)?,
            created_at: Timestamp::UNIX_EPOCH,
            superseded_by: r.get(11)?,
            log_url: r.get(12)?,
        },
        r.get(10)?,
    ))
}

fn list_summaries(
    conn: &Connection,
    sql: &str,
    args: impl rusqlite::Params,
) -> Result<Vec<ExecutionSummary>> {
    let mut stmt = conn.prepare(sql)?;
    let rows = stmt
        .query_map(args, summary_row)?
        .collect::<rusqlite::Result<Vec<_>>>()?;
    rows.into_iter()
        .map(|(mut e, created_at)| {
            e.created_at = time::from_sql(&created_at)?;
            Ok(e)
        })
        .collect()
}

fn pr_meta_row(r: &Row<'_>) -> rusqlite::Result<(PrMeta, String)> {
    Ok((
        PrMeta {
            repo: r.get(0)?,
            pr: r.get(1)?,
            title: r.get(2)?,
            body: r.get(3)?,
            author_login: r.get(4)?,
            head_ref: r.get(5)?,
            url: r.get(6)?,
            auto_merge: r.get(7)?,
            updated_at: None,
        },
        r.get(8)?,
    ))
}

fn applylock_row(r: &Row<'_>) -> rusqlite::Result<ApplyLockCheck> {
    let stacks: String = r.get(5)?;
    Ok(ApplyLockCheck {
        environment: r.get(0)?,
        head_sha: r.get(1)?,
        check_run_id: r.get(2)?,
        pr: r.get(3)?,
        repo: r.get(4)?,
        // Go ignores an unparseable stacks column.
        stacks: serde_json::from_str::<Option<Vec<String>>>(&stacks)
            .ok()
            .flatten(),
        state: r.get(6)?,
        kind: r.get(7)?,
        execution_id: r.get(8)?,
    })
}

impl Store {
    /// Runs read-only SQL against the connection. The HTTP layer's inspect
    /// endpoints query the tables directly, as Go's do.
    pub fn read<T>(&self, f: impl FnOnce(&Connection) -> rusqlite::Result<T>) -> Result<T> {
        Ok(f(&self.conn())?)
    }

    /// Runs SQL in an immediate transaction (tests seed fixtures with it).
    pub fn exec<T>(&self, f: impl FnOnce(&Connection) -> rusqlite::Result<T>) -> Result<T> {
        self.write(|tx| Ok(f(tx)?))
    }

    /// Records the PR's current metadata, replacing the (repo, pr) row.
    pub fn upsert_pr_meta(&self, m: &PrMeta) -> Result<()> {
        self.write(|tx| {
            tx.execute(
                "INSERT INTO pr_meta (repo, pr, title, body, author_login, head_ref, url, auto_merge)
                 VALUES (?1,?2,?3,?4,?5,?6,?7,?8)
                 ON CONFLICT(repo, pr) DO UPDATE SET
                   title=excluded.title, body=excluded.body, author_login=excluded.author_login,
                   head_ref=excluded.head_ref, url=excluded.url, auto_merge=excluded.auto_merge,
                   updated_at=CURRENT_TIMESTAMP",
                params![m.repo, m.pr, m.title, m.body, m.author_login, m.head_ref, m.url, m.auto_merge],
            )?;
            Ok(())
        })
    }

    /// The metadata row of (repo, pr), if any.
    pub fn get_pr_meta(&self, repo: &str, pr: i64) -> Result<Option<PrMeta>> {
        self.pr_meta_query(
            "SELECT repo, pr, title, body, author_login, head_ref, url, auto_merge, updated_at
             FROM pr_meta WHERE repo = ?1 AND pr = ?2",
            params![repo, pr],
        )
    }

    /// The metadata row of `pr` in any repo, the most recently updated one winning.
    /// The webhook writes it before any execution exists, so the lookup must not
    /// need a repo.
    pub fn get_pr_meta_by_pr(&self, pr: i64) -> Result<Option<PrMeta>> {
        self.pr_meta_query(
            "SELECT repo, pr, title, body, author_login, head_ref, url, auto_merge, updated_at
             FROM pr_meta WHERE pr = ?1 ORDER BY updated_at DESC LIMIT 1",
            params![pr],
        )
    }

    fn pr_meta_query(&self, sql: &str, args: impl rusqlite::Params) -> Result<Option<PrMeta>> {
        let row = self.conn().query_row(sql, args, pr_meta_row).optional()?;
        row.map(|(mut m, at)| {
            m.updated_at = Some(time::from_sql(&at)?);
            Ok(m)
        })
        .transpose()
    }

    /// Records (or replaces) the pointer and tail excerpt of one stack's output of
    /// a kind (`log` or `plan`).
    pub fn upsert_stack_output(
        &self,
        exec_id: &str,
        stack: &str,
        kind: &str,
        pointer: &str,
        excerpt: &str,
    ) -> Result<()> {
        self.write(|tx| {
            tx.execute(
                "INSERT INTO stack_outputs (execution_id, stack_path, kind, pointer, excerpt)
                 VALUES (?1,?2,?3,?4,?5)
                 ON CONFLICT(execution_id, stack_path, kind) DO UPDATE SET
                   pointer    = excluded.pointer,
                   excerpt    = excluded.excerpt,
                   updated_at = CURRENT_TIMESTAMP",
                params![exec_id, stack, kind, pointer, excerpt],
            )?;
            Ok(())
        })
    }

    /// The (pointer, excerpt) of a stack's output kind, if recorded.
    pub fn get_stack_output(
        &self,
        exec_id: &str,
        stack: &str,
        kind: &str,
    ) -> Result<Option<(String, String)>> {
        Ok(self
            .conn()
            .query_row(
                "SELECT COALESCE(pointer,''), COALESCE(excerpt,'') FROM stack_outputs
                 WHERE execution_id = ?1 AND stack_path = ?2 AND kind = ?3",
                params![exec_id, stack, kind],
                |r| Ok((r.get(0)?, r.get(1)?)),
            )
            .optional()?)
    }

    /// Records an apply-lock check, replacing the (environment, head SHA) row.
    pub fn upsert_apply_lock_check(&self, c: &ApplyLockCheck) -> Result<()> {
        let stacks = serde_json::to_string(&c.stacks)?;
        self.write(|tx| {
            tx.execute(
                "INSERT INTO applylock_checks (environment, head_sha, check_run_id, pr, repo, stacks, state, kind, execution_id, updated_at)
                 VALUES (?1,?2,?3,?4,?5,?6,?7,?8,?9,CURRENT_TIMESTAMP)
                 ON CONFLICT(environment, head_sha) DO UPDATE SET
                   check_run_id=excluded.check_run_id, pr=excluded.pr, repo=excluded.repo, stacks=excluded.stacks,
                   state=excluded.state, kind=excluded.kind, execution_id=excluded.execution_id, updated_at=CURRENT_TIMESTAMP",
                params![
                    c.environment,
                    c.head_sha,
                    c.check_run_id,
                    c.pr,
                    c.repo,
                    stacks,
                    c.state,
                    c.kind,
                    c.execution_id
                ],
            )?;
            Ok(())
        })
    }

    /// The apply-lock check of (environment, head SHA), if any.
    pub fn get_apply_lock_check(
        &self,
        env: &str,
        head_sha: &str,
    ) -> Result<Option<ApplyLockCheck>> {
        Ok(self
            .conn()
            .query_row(
                "SELECT environment, head_sha, check_run_id, pr, repo, stacks, state, kind, execution_id
                 FROM applylock_checks WHERE environment = ?1 AND head_sha = ?2",
                params![env, head_sha],
                applylock_row,
            )
            .optional()?)
    }

    /// Every held apply-lock check of `env`.
    pub fn held_apply_lock_checks(&self, env: &str) -> Result<Vec<ApplyLockCheck>> {
        let conn = self.conn();
        let mut stmt = conn.prepare(
            "SELECT environment, head_sha, check_run_id, pr, repo, stacks, state, kind, execution_id
             FROM applylock_checks WHERE environment = ?1 AND state = 'held'",
        )?;
        let rows = stmt
            .query_map([env], applylock_row)?
            .collect::<rusqlite::Result<Vec<_>>>()?;
        Ok(rows)
    }

    /// Stores the rendered plan report of an execution.
    pub fn set_report(&self, id: &str, markdown: &str) -> Result<()> {
        self.write(|tx| {
            tx.execute(
                "UPDATE executions SET report_markdown = ?1 WHERE id = ?2",
                params![markdown, id],
            )?;
            Ok(())
        })
    }

    /// Stores an execution's change reasons as JSON (`[]` when there are none).
    pub fn set_change_reasons(&self, id: &str, reasons: &[ChangeReason]) -> Result<()> {
        let json = serde_json::to_string(&reasons.iter().map(GoReason).collect::<Vec<_>>())?;
        self.write(|tx| {
            tx.execute(
                "UPDATE executions SET change_reasons = ?1 WHERE id = ?2",
                params![json, id],
            )?;
            Ok(())
        })
    }

    /// Increments an execution's cache-bust revision.
    pub fn bump_rev(&self, id: &str) -> Result<()> {
        self.write(|tx| {
            tx.execute("UPDATE executions SET rev = rev + 1 WHERE id = ?1", [id])?;
            Ok(())
        })
    }

    /// Records the GitHub check-run id of an execution.
    pub fn set_check_run_id(&self, id: &str, check_run_id: i64) -> Result<()> {
        self.write(|tx| {
            tx.execute(
                "UPDATE executions SET check_run_id = ?1 WHERE id = ?2",
                params![check_run_id, id],
            )?;
            Ok(())
        })
    }

    /// The newest executions, newest first.
    pub fn list_executions(&self, limit: i64) -> Result<Vec<ExecutionSummary>> {
        list_summaries(
            &self.conn(),
            &format!(
                "SELECT {LIST_EXECUTION_COLUMNS} FROM executions ORDER BY created_at DESC, id DESC LIMIT ?1"
            ),
            [limit],
        )
    }

    /// Every execution of `pr`, newest first.
    pub fn list_executions_for_pr(&self, pr: i64) -> Result<Vec<ExecutionSummary>> {
        list_summaries(
            &self.conn(),
            &format!(
                "SELECT {LIST_EXECUTION_COLUMNS} FROM executions WHERE pr = ?1 ORDER BY created_at DESC, id DESC"
            ),
            [pr],
        )
    }

    /// The distinct non-empty environments `pr` has executions in (unordered).
    pub fn environments_for_pr(&self, pr: i64) -> Result<Vec<String>> {
        let conn = self.conn();
        let mut stmt = conn.prepare(
            "SELECT DISTINCT environment FROM executions WHERE pr = ?1 AND environment != ''",
        )?;
        let rows = stmt
            .query_map([pr], |r| r.get(0))?
            .collect::<rusqlite::Result<Vec<String>>>()?;
        Ok(rows)
    }

    /// The newest verify-context execution of (pr, environment). Go treats a query
    /// error as "none".
    pub fn latest_verify_execution_id(&self, pr: i64, environment: &str) -> Option<String> {
        self.optional_id(
            "SELECT id FROM executions
             WHERE pr = ?1 AND environment = ?2 AND status_context LIKE 'verify%'
             ORDER BY created_at DESC, id DESC LIMIT 1",
            params![pr, environment],
        )
    }

    /// The newest non-superseded execution of (pr, environment, sha, context) other
    /// than `incoming_id`; none when `pr <= 0`.
    pub fn find_non_superseded_execution(
        &self,
        pr: i64,
        environment: &str,
        sha: &str,
        status_context: &str,
        incoming_id: &str,
    ) -> Result<Option<String>> {
        if pr <= 0 {
            return Ok(None);
        }
        Ok(self
            .conn()
            .query_row(
                "SELECT id FROM executions
                 WHERE pr = ?1 AND environment = ?2 AND sha = ?3 AND status_context = ?4
                   AND id != ?5 AND (superseded_by IS NULL OR superseded_by = '')
                 ORDER BY created_at DESC, id DESC LIMIT 1",
                params![pr, environment, sha, status_context, incoming_id],
                |r| r.get(0),
            )
            .optional()?)
    }

    /// The newest non-superseded execution of (environment, context, sha) with a PR.
    pub fn find_execution_by_sha(
        &self,
        environment: &str,
        status_context: &str,
        sha: &str,
    ) -> Result<Option<String>> {
        Ok(self
            .conn()
            .query_row(
                "SELECT id FROM executions
                 WHERE environment = ?1 AND status_context = ?2 AND sha = ?3 AND COALESCE(pr,0) > 0
                   AND (superseded_by IS NULL OR superseded_by = '')
                 ORDER BY created_at DESC, id DESC LIMIT 1",
                params![environment, status_context, sha],
                |r| r.get(0),
            )
            .optional()?)
    }

    /// This environment's serve-started executions still queued (in progress, no
    /// phase, not superseded) and created before `cutoff`: the start watchdog's
    /// candidates.
    pub fn stuck_pending_executions(
        &self,
        environment: &str,
        cutoff: Timestamp,
    ) -> Result<Vec<ExecutionSummary>> {
        let conn = self.conn();
        let mut stmt = conn.prepare(
            "SELECT id, repo, sha, COALESCE(pr,0), COALESCE(environment,''), COALESCE(status,''),
                    COALESCE(status_context,''), COALESCE(phase,''), created_at
               FROM executions
              WHERE status = 'in_progress' AND COALESCE(phase,'') = ''
                AND COALESCE(superseded_by,'') = ''
                AND environment = ?1
                AND id LIKE 'run-%'
                AND created_at < ?2",
        )?;
        let rows = stmt
            .query_map(params![environment, time::to_sql(cutoff)], |r| {
                Ok((
                    ExecutionSummary {
                        id: r.get(0)?,
                        repo: r.get(1)?,
                        sha: r.get(2)?,
                        pr: r.get(3)?,
                        environment: r.get(4)?,
                        status: r.get(5)?,
                        status_context: r.get(6)?,
                        phase: r.get(7)?,
                        progress_label: None,
                        progress_pct: None,
                        created_at: Timestamp::UNIX_EPOCH,
                        superseded_by: String::new(),
                        log_url: String::new(),
                    },
                    r.get::<_, String>(8)?,
                ))
            })?
            .collect::<rusqlite::Result<Vec<_>>>()?;
        rows.into_iter()
            .map(|(mut e, at)| {
                e.created_at = time::from_sql(&at)?;
                Ok(e)
            })
            .collect()
    }

    /// The newest execution of (pr, environment) the runner reported on (a phase or
    /// registered stacks). Go treats a query error as "none".
    pub fn latest_reported_execution_id(&self, pr: i64, environment: &str) -> Option<String> {
        self.optional_id(
            "SELECT id FROM executions WHERE pr = ?1 AND environment = ?2
               AND (COALESCE(phase,'') != ''
                    OR EXISTS (SELECT 1 FROM stacks WHERE execution_id = executions.id))
             ORDER BY created_at DESC, id DESC LIMIT 1",
            params![pr, environment],
        )
    }

    /// The PR of the newest execution of (sha, environment, context) with a PR.
    pub fn latest_pr_for_sha(
        &self,
        sha: &str,
        environment: &str,
        status_context: &str,
    ) -> Option<i64> {
        self.conn()
            .query_row(
                "SELECT COALESCE(pr,0) FROM executions
                  WHERE sha = ?1 AND environment = ?2 AND COALESCE(status_context,'') = ?3 AND COALESCE(pr,0) > 0
                 ORDER BY created_at DESC, id DESC LIMIT 1",
                params![sha, environment, status_context],
                |r| r.get::<_, i64>(0),
            )
            .ok()
            .filter(|pr| *pr != 0)
    }

    fn optional_id(&self, sql: &str, args: impl rusqlite::Params) -> Option<String> {
        self.conn().query_row(sql, args, |r| r.get(0)).ok()
    }
}

/// A change reason as Go marshals it: a nil `via` is `null`.
struct GoReason<'a>(&'a ChangeReason);

impl serde::Serialize for GoReason<'_> {
    fn serialize<S: serde::Serializer>(&self, s: S) -> std::result::Result<S::Ok, S::Error> {
        use serde::ser::SerializeStruct;
        let mut st = s.serialize_struct("ChangeReason", 3)?;
        st.serialize_field("stack", &self.0.stack)?;
        st.serialize_field("kind", &self.0.kind)?;
        if self.0.via.is_empty() {
            st.serialize_field("via", &Option::<()>::None)?;
        } else {
            st.serialize_field("via", &self.0.via)?;
        }
        st.end()
    }
}

#[cfg(test)]
mod tests;
