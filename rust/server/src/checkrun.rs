//! Driving the GitHub surface after a state change (Go: `checkrun.go` and
//! `loadSnapshot` in `status.go`): the plan gate's check run, the post-merge
//! apply's check run or commit status, and the SSE notification.

use std::sync::Arc;

use tfstackplan_gate::approval::GrantState;
use tfstackplan_gate::events::{Graph, Phase, Status};
use tfstackplan_gate::execution::{ReportFail, ReportSucceed};
use tfstackplan_gate::reconcile::is_run_execution_id;
use tfstackplan_server_clients::github::CheckRunUpdate;
use tfstackplan_store::{ApplyLockCheck, ExecutionRow, Store};
use tracing::warn;

use crate::render::{
    ApplyLockVerdict, Snapshot, check_summary, conclusion, error_tail, failures_section,
    gates_section, is_apply_context, is_gate, lock_blocked, lock_section, lock_title,
    progress_title,
};
use crate::{App, Error, Result};

/// The verdict input of an execution, purely from the store.
pub(crate) fn load_snapshot(
    s: &Store,
    id: &str,
) -> tfstackplan_store::Result<Option<(Snapshot, ExecutionRow)>> {
    let Some(exec) = s.get_execution(id)? else {
        return Ok(None);
    };
    let mut snap = Snapshot {
        phase: Phase::new(&exec.phase),
        finalized: !exec.report_markdown.is_empty(),
        ..Snapshot::default()
    };
    if let Ok(g) = s.load_graph(id) {
        for st in &g.stacks {
            snap.total_stacks += 1;
            if st.status != Status::Pending && st.status != Status::Running {
                snap.planned_stacks += 1;
            }
            if st.status == Status::Failed {
                snap.any_failed = true;
            }
        }
    }
    if exec.pr != 0
        && let Ok(targets) = s.targets_for(exec.pr, &exec.environment)
    {
        snap.total_gates = targets.len();
        for t in &targets {
            match t.state.as_str() {
                s if s == GrantState::Active.as_str() => snap.active_gates += 1,
                "DENIED" | "REVOKED" | "EXPIRED" => snap.denied_gates += 1,
                _ => {}
            }
        }
    }
    Ok(Some((snap, exec)))
}

/// Replaces a failed stack's detail with its stored log's error tail.
pub(crate) fn backfill_failure_detail(s: &Store, exec_id: &str, g: &mut Graph) {
    for st in g.stacks.iter_mut().filter(|st| st.status == Status::Failed) {
        if let Ok(Some((_, excerpt))) = s.get_stack_output(exec_id, &st.path, "log")
            && !excerpt.is_empty()
        {
            st.detail = error_tail(&excerpt, 25);
        }
    }
}

impl App {
    /// Creates the execution's check run unless it has one, and records its id.
    pub(crate) async fn ensure_check_run(
        &self,
        id: &str,
        repo: &str,
        sha: &str,
        name: &str,
        details_url: &str,
    ) -> Result<()> {
        let id_owned = id.to_string();
        let e = self
            .db(move |s| s.get_execution(&id_owned))
            .await?
            .ok_or_else(|| Error::Message("sql: no rows in result set".into()))?;
        if e.check_run_id.is_some_and(|c| c != 0) {
            return Ok(());
        }
        let cr = self
            .gh
            .create_check_run(repo, sha, name, details_url)
            .await
            .map_err(|e| Error::Message(e.to_string()))?;
        let id_owned = id.to_string();
        self.db(move |s| s.set_check_run_id(&id_owned, cr))
            .await
            .map_err(|e| Error::Message(format!("persist check_run_id: {e}")))
    }

    /// Updates the GitHub surface for an execution and notifies live pages.
    pub(crate) async fn drive(self: &Arc<Self>, id: &str, base: &str, terminal: bool) {
        let id_owned = id.to_string();
        match self.db(move |s| s.get_execution(&id_owned)).await {
            Ok(Some(e)) if is_apply_context(&e.status_context) => self.drive_apply(e, base).await,
            _ => self.render_and_patch(id, terminal).await,
        }
        self.hub.publish(&format!("exec:{id}"), "changed");
    }

    /// Bumps the revision and patches the plan gate's check run.
    async fn render_and_patch(self: &Arc<Self>, id: &str, terminal: bool) {
        let id_owned = id.to_string();
        let loaded = self
            .db(move |s| {
                s.bump_rev(&id_owned)?;
                let Some(e) = s.get_execution(&id_owned)? else {
                    return Ok(None);
                };
                if e.check_run_id.is_none_or(|c| c == 0) {
                    return Ok(Some((e, None)));
                }
                let mut g = s.load_graph(&id_owned)?;
                backfill_failure_detail(s, &id_owned, &mut g);
                let targets = s.targets_for(e.pr, &e.environment).unwrap_or_default();
                Ok(Some((e, Some((g, targets)))))
            })
            .await;
        let (e, g, targets) = match loaded {
            Ok(Some((e, Some((g, targets))))) => (e, g, targets),
            Ok(_) => return,
            Err(err) => {
                warn!("render {id}: {err}");
                return;
            }
        };
        let Some(check_run_id) = e.check_run_id else {
            return;
        };
        let mut lock = ApplyLockVerdict::default();
        let mut pending_clear = None;
        if terminal
            && self.run_trigger_armed()
            && e.pr > 0
            && is_gate(&e.status_context, &e.environment)
        {
            let stacks: Vec<String> = g.stacks.iter().map(|s| s.path.clone()).collect();
            lock = self
                .eval_apply_lock(&e.environment, e.pr, &stacks, self.now())
                .await;
            let rec = ApplyLockCheck {
                environment: e.environment.clone(),
                head_sha: e.sha.clone(),
                check_run_id,
                pr: e.pr,
                repo: e.repo.clone(),
                stacks: Some(stacks),
                state: lock.state.clone(),
                kind: "pr_head".into(),
                execution_id: e.id.clone(),
            };
            if lock.state == "held" || lock.state == "unverifiable" {
                if let Err(err) = self.db(move |s| s.upsert_apply_lock_check(&rec)).await {
                    warn!("consolidated lock record {}: {err}", e.id);
                }
            } else {
                // Persisted only after the patch lands: a clear record drops the row
                // out of the held set, and nothing would re-drive a failed patch.
                pending_clear = Some(rec);
            }
        }
        let ui = self.ui_url(e.pr, id);
        let phase = Phase::new(&e.phase);
        let mut upd = CheckRunUpdate {
            title: progress_title(
                self.cfg.progress.as_ref(),
                &phase,
                &g.stacks,
                terminal,
                "plan",
            ),
            summary: check_summary("plan", &g.stacks, &ui, &phase),
            text: format!(
                "{}{}{}{}",
                lock_section(&lock),
                gates_section(&targets),
                failures_section(&g, &e.log_url, ""),
                e.report_markdown
            ),
            details_url: ui,
            conclusion: String::new(),
        };
        if e.phase.is_empty() && g.stacks.is_empty() && is_run_execution_id(id) {
            upd.title = if e.status == "failure" {
                "build failed to start — use Re-run to retry".into()
            } else {
                "queued — waiting for the build to start".into()
            };
        }
        let id_owned = id.to_string();
        let snap = self
            .db(move |s| load_snapshot(s, &id_owned))
            .await
            .ok()
            .flatten()
            .map(|(snap, _)| snap);
        if terminal {
            if e.status == "failure" && g.stacks.is_empty() {
                upd.conclusion = "failure".into();
            } else if let Some(snap) = &snap {
                upd.conclusion = conclusion(snap, &lock).into();
                if upd.conclusion.is_empty()
                    && lock_blocked(&lock)
                    && conclusion(snap, &ApplyLockVerdict::default()) == "success"
                {
                    upd.title = lock_title(&lock);
                }
            }
        }
        // Go re-reads the snapshot here; nothing between the two reads writes.
        if let Some(snap) = &snap
            && snap.awaiting_approval()
        {
            upd.title = format!(
                "awaiting approval — {} of {} gates active",
                snap.active_gates, snap.total_gates
            );
        }
        if let Err(err) = self.gh.update_check_run(&e.repo, check_run_id, &upd).await {
            warn!("update check run {id}: {err}");
            return;
        }
        if let Some(rec) = pending_clear
            && let Err(err) = self.db(move |s| s.upsert_apply_lock_check(&rec)).await
        {
            warn!("consolidated lock record {}: {err}", e.id);
        }
    }

    /// Updates the GitHub surface of a post-merge apply.
    async fn drive_apply(self: &Arc<Self>, e: ExecutionRow, base: &str) {
        let id = e.id.clone();
        let g = match self
            .db(move |s| {
                let mut g = s.load_graph(&id)?;
                backfill_failure_detail(s, &id, &mut g);
                Ok(g)
            })
            .await
        {
            Ok(g) => g,
            Err(err) => {
                warn!("apply status: load graph {}: {err}", e.id);
                return;
            }
        };
        let total = g.stacks.len();
        let applied = g
            .stacks
            .iter()
            .filter(|s| matches!(s.status, Status::Safe | Status::Nochange | Status::Moving))
            .count();
        let failed = g
            .stacks
            .iter()
            .filter(|s| s.status == Status::Failed)
            .count();
        let (state, desc) = if failed > 0 || e.status == "failure" {
            (
                "failure",
                format!(
                    "apply failed — {applied}/{total} applied, {failed} failed; see the failing stack below — fix-forward or re-run this tier's apply"
                ),
            )
        } else if total == 0 && e.phase.is_empty() && is_run_execution_id(&e.id) {
            (
                "pending",
                "queued — waiting for the build to start".to_string(),
            )
        } else if total == 0 {
            ("success", "no stacks to apply".to_string())
        } else if applied == total {
            ("success", format!("applied {applied}/{total} stacks"))
        } else {
            ("pending", format!("applying… {applied}/{total} stacks"))
        };
        let signal = match state {
            "success" => Some(ReportSucceed {}.into()),
            "failure" => Some(ReportFail {}.into()),
            _ => None,
        };
        if let Some(sig) = signal
            && let Err(err) = self.handle_exec(&e.id, sig).await
        {
            warn!("apply set status {}: {err}", e.id);
        }
        let ui = self.ui_url(e.pr, &e.id);
        if let Some(cr) = e.check_run_id.filter(|c| *c != 0) {
            let concl = match state {
                "success" | "failure" => state.to_string(),
                _ => String::new(),
            };
            let terminal = !concl.is_empty();
            let phase = Phase::new(&e.phase);
            let mut summary = check_summary("apply", &g.stacks, &ui, &phase);
            if failed > 0 {
                summary.push_str("\n\n");
                summary.push_str(&desc);
            }
            let upd = CheckRunUpdate {
                title: progress_title(
                    self.cfg.progress.as_ref(),
                    &phase,
                    &g.stacks,
                    terminal,
                    "apply",
                ),
                summary,
                text: if failed > 0 {
                    failures_section(
                        &g,
                        &e.log_url,
                        &format!("{}/logs/{}", base.trim_end_matches('/'), e.id),
                    )
                } else {
                    String::new()
                },
                details_url: ui,
                conclusion: concl,
            };
            if let Err(err) = self.gh.update_check_run(&e.repo, cr, &upd).await {
                warn!("apply check run {}: {err}", e.id);
            }
            return;
        }
        if let Err(err) = self
            .gh
            .post_status(&e.repo, &e.sha, &e.status_context, state, &desc, &ui)
            .await
        {
            warn!("apply status {}: {err}", e.id);
        }
    }
}

#[cfg(test)]
mod tests;
