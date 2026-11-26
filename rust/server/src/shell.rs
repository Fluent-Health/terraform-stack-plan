//! The imperative shell (Go: `shell.go`, `shell_execute.go`, `shell_run.go`,
//! `shell_exec.go`, `claimshell.go`): one signal for a (PR, environment) runs to a
//! fixpoint under that key's lock: decide, fold and persist (the store's
//! `step_gate`), run the React actions, feed their results back as signals.
//!
//! Lock order is one-directional, as in Go: a gate round may take an execution
//! lock (materialising a run) or an environment lock (releasing claims), never the
//! reverse, so the aggregates cannot deadlock.

use std::collections::VecDeque;
use std::sync::Arc;

use tfstackplan_gate::approval::{GrantState, Request};
use tfstackplan_gate::claims;
use tfstackplan_gate::events::{Init, Status};
use tfstackplan_gate::execution::{self, ReportInit, ReportSupersede};
use tfstackplan_gate::reconcile::{
    Action, AdoptRun, CancelRun, ChangeSet, Collision, GateState, GateTick, GrantsObserved,
    ObservedGrant, RUN_KIND_APPLY, RequestGrant, RunStartResult, Signal, StartRun,
};
use tfstackplan_server_clients::Error as ClientError;
use tfstackplan_server_clients::executor::{Ref, RunRequest};
use tfstackplan_server_clients::github::CheckRunUpdate;
use tracing::warn;

use crate::render::run_context;
use crate::{App, Result};

const MAX_ITERS: usize = 16;

/// Maps the wire Init onto the execution aggregate's state (Go: execInitFromEvents).
/// A missing stack status reads pending, and the run starts in_progress.
pub(crate) fn exec_init_from_events(init: &Init) -> execution::State {
    execution::State {
        id: init.id.clone(),
        repo: init.repo.clone(),
        sha: init.sha.clone(),
        pr: init.pr,
        environment: init.environment.clone(),
        context: init.context.clone(),
        log_url: init.log_url.clone(),
        status: "in_progress".into(),
        stacks: init
            .stacks
            .iter()
            .map(|s| execution::Stack {
                path: s.path.clone(),
                project: s.project.clone(),
                run_status: if s.status == Status::Unset {
                    Status::Pending
                } else {
                    s.status
                },
                counts: s.counts,
                ..execution::Stack::default()
            })
            .collect(),
        edges: init
            .edges
            .iter()
            .map(|e| execution::Edge {
                from: e.from.clone(),
                to: e.to.clone(),
            })
            .collect(),
        ..execution::State::default()
    }
}

impl App {
    /// Runs `sig` for (pr, env) to a fixpoint under the key's lock.
    pub(crate) async fn handle(
        self: &Arc<Self>,
        pr: i64,
        env: &str,
        repo: &str,
        sig: Signal,
    ) -> Result<()> {
        let _guard = self.gate_locks.lock((pr, env.to_string())).await;
        let mut queue = VecDeque::from([sig]);
        for _ in 0..MAX_ITERS {
            let Some(cur) = queue.pop_front() else { break };
            let env_owned = env.to_string();
            let step = self.db(move |s| s.step_gate(pr, &env_owned, &cur)).await?;
            let feedback = self.execute(&step.state, repo, step.actions).await;
            queue.extend(feedback);
        }
        if !queue.is_empty() {
            warn!("shell: maxIters={MAX_ITERS} reached for pr={pr} env={env}");
        }
        Ok(())
    }

    /// A full grant re-list for the change set's stored targets, run as a GateTick.
    /// A backend error aborts the tick: an omitted target would read as a vanished
    /// grant and downgrade a satisfied gate.
    pub(crate) async fn tick(self: &Arc<Self>, pr: i64, env: &str) -> Result<()> {
        let env_owned = env.to_string();
        let targets = self.db(move |s| s.targets_for(pr, &env_owned)).await?;
        if targets.is_empty() {
            return Ok(());
        }
        let mut obs = Vec::new();
        if let Some(approval) = &self.approval {
            for t in &targets {
                let grants = approval
                    .list_grants(&t.class, &t.target)
                    .await
                    .map_err(|e| {
                        crate::Error::Message(format!(
                            "tick: list grants {}/{} pr={pr} env={env}: {e}",
                            t.class, t.target
                        ))
                    })?;
                for g in grants {
                    if g.request.pr == pr && g.request.environment == env {
                        obs.push(ObservedGrant {
                            class: t.class.clone(),
                            target: t.target.clone(),
                            name: g.name,
                            state: g.state,
                            requester: g.requester,
                            collision: None,
                        });
                    }
                }
            }
        }
        let env_owned = env.to_string();
        let repo = self
            .db(move |s| {
                Ok(match s.latest_execution_id(pr, &env_owned)? {
                    Some(id) => s.get_execution(&id)?.map(|e| e.repo).unwrap_or_default(),
                    None => String::new(),
                })
            })
            .await
            .unwrap_or_default();
        self.handle(pr, env, &repo, GateTick { grants: obs }.into())
            .await
    }

    /// Runs one execution-lifecycle signal under the execution's lock.
    pub(crate) async fn handle_exec(&self, exec_id: &str, sig: execution::Signal) -> Result<()> {
        let _guard = self.exec_locks.lock(exec_id.to_string()).await;
        let id = exec_id.to_string();
        self.db(move |s| s.step_execution(&id, &sig)).await?;
        Ok(())
    }

    /// Applies one claim command to `env`'s ledger under the environment's lock.
    pub(crate) async fn handle_claim(&self, env: &str, cmd: claims::Command) -> Result<()> {
        let _guard = self.env_locks.lock(env.to_string()).await;
        let env_owned = env.to_string();
        self.db(move |s| s.step_claims(&env_owned, &cmd)).await?;
        Ok(())
    }

    /// Runs the actions and returns the feedback signals for the next iteration.
    async fn execute(
        self: &Arc<Self>,
        cs: &ChangeSet,
        repo: &str,
        actions: Vec<Action>,
    ) -> Vec<Signal> {
        let mut grants = Vec::new();
        let mut feedback = Vec::new();
        for a in actions {
            match a {
                Action::RequestGrant(act) => {
                    let Some(approval) = &self.approval else {
                        continue;
                    };
                    let req = Request {
                        class: act.class.clone(),
                        target: act.target.clone(),
                        pr: cs.pr,
                        environment: cs.environment.clone(),
                        requester: act.requester.clone(),
                    };
                    match approval.request_grant(&req).await {
                        Ok(g) => grants.push(ObservedGrant {
                            class: act.class,
                            target: act.target,
                            name: g.name,
                            state: g.state,
                            requester: g.requester,
                            collision: None,
                        }),
                        Err(e) => grants.push(self.observe_error(cs, repo, &act, e).await),
                    }
                }
                Action::RevokeGrant(act) => {
                    let Some(approval) = &self.approval else {
                        continue;
                    };
                    let req = Request {
                        class: act.class.clone(),
                        target: act.target.clone(),
                        pr: act.pr,
                        environment: act.environment.clone(),
                        requester: String::new(),
                    };
                    if let Err(e) = approval.revoke(&req).await {
                        warn!(
                            "shell: revoke pr={} env={} {}/{}: {e}",
                            act.pr, act.environment, act.class, act.target
                        );
                    }
                }
                Action::StartRun(act) => feedback.push(self.start_run(cs, repo, act).await),
                Action::CancelRun(act) => self.cancel_run(act).await,
                Action::AdoptRun(act) => self.adopt_run(cs, repo, act).await,
                Action::RenderCheckRun(act) => {
                    let (pr, env) = (cs.pr, cs.environment.clone());
                    if let Ok(Some(id)) = self.db(move |s| s.latest_execution_id(pr, &env)).await {
                        let base = self.cfg.public_base_url.trim_end_matches('/').to_string();
                        self.drive(&id, &base, act.terminal).await;
                    }
                }
                // drive() posts the commit status on terminal renders already.
                Action::PostCommitStatus(_) => {}
                Action::PublishSSE(_) => {
                    let (pr, env) = (cs.pr, cs.environment.clone());
                    if let Ok(Some(id)) = self.db(move |s| s.latest_execution_id(pr, &env)).await {
                        self.hub.publish(&format!("exec:{id}"), "changed");
                    }
                }
                Action::ReleaseClaim(act) => {
                    self.release_apply_claims(&act.environment, act.pr).await
                }
            }
        }
        if !grants.is_empty() {
            feedback.push(GrantsObserved { grants }.into());
        }
        feedback
    }

    /// Maps a RequestGrant error: a slot collision is resolved against GitHub
    /// (self, or an abandoned PR); any other error leaves the target pending.
    async fn observe_error(
        &self,
        cs: &ChangeSet,
        repo: &str,
        act: &RequestGrant,
        err: ClientError,
    ) -> ObservedGrant {
        let ClientError::SlotCollision(col) = err else {
            warn!(
                "shell: request grant pr={} env={} {}/{}: {err}",
                cs.pr, cs.environment, act.class, act.target
            );
            return ObservedGrant {
                class: act.class.clone(),
                target: act.target.clone(),
                state: GrantState::Absent,
                ..ObservedGrant::default()
            };
        };
        let b = &col.blocking_grant.request;
        let by_self = b.pr == cs.pr;
        let mut abandoned = false;
        if !by_self && b.pr > 0 {
            match self.gh.pr_abandoned(repo, b.pr).await {
                Ok(c) => abandoned = c,
                Err(e) => warn!("shell: slot-collision PRAbandoned({}): {e}", b.pr),
            }
        }
        ObservedGrant {
            class: act.class.clone(),
            target: act.target.clone(),
            collision: Some(Collision {
                by_pr: b.pr,
                by_env: b.environment.clone(),
                by_self,
                by_pr_abandoned: abandoned,
            }),
            ..ObservedGrant::default()
        }
    }

    /// Creates the queued execution row and its check run for a run. Only the row
    /// write can fail; the check run is best-effort.
    async fn materialize_run(
        &self,
        cs: &ChangeSet,
        repo: &str,
        exec_id: &str,
        sha: &str,
        kind: &str,
    ) -> Result<()> {
        let init = Init {
            id: exec_id.into(),
            repo: repo.into(),
            sha: sha.into(),
            pr: cs.pr,
            environment: cs.environment.clone(),
            context: run_context(kind, &cs.environment),
            ..Init::default()
        };
        self.handle_exec(
            exec_id,
            ReportInit {
                exec: exec_init_from_events(&init),
            }
            .into(),
        )
        .await?;
        let name = if kind == RUN_KIND_APPLY {
            init.context.clone()
        } else {
            self.plan_check_name(&cs.environment)
        };
        if let Err(e) = self
            .ensure_check_run(exec_id, repo, sha, &name, &self.ui_url(cs.pr, exec_id))
            .await
        {
            warn!("shell: check run {exec_id}: {e}");
        }
        Ok(())
    }

    async fn start_run(&self, cs: &ChangeSet, repo: &str, act: StartRun) -> Signal {
        let mut result = RunStartResult {
            kind: act.kind.clone(),
            execution_id: act.execution_id.clone(),
            ..RunStartResult::default()
        };
        if let Err(e) = self
            .materialize_run(cs, repo, &act.execution_id, &act.sha, &act.kind)
            .await
        {
            result.err = format!("store queued execution: {e}");
            return result.into();
        }
        let Some(executor) = &self.executor else {
            result.err = "no executor configured".into();
            return result.into();
        };
        match executor
            .start(&RunRequest {
                kind: act.kind,
                environment: cs.environment.clone(),
                sha: act.sha,
                branch: act.branch,
                execution_id: act.execution_id,
                pr: cs.pr,
            })
            .await
        {
            Ok(r) => result.build_ref = r.id,
            Err(e) => result.err = e.to_string(),
        }
        result.into()
    }

    async fn adopt_run(&self, cs: &ChangeSet, repo: &str, act: AdoptRun) {
        if let Err(e) = self
            .materialize_run(cs, repo, &act.execution_id, &act.sha, &act.kind)
            .await
        {
            warn!(
                "shell: adopt run {}: store execution: {e}",
                act.execution_id
            );
        }
    }

    async fn cancel_run(self: &Arc<Self>, act: CancelRun) {
        self.supersede_execution(&act.old_execution_id, &act.new_execution_id)
            .await;
        let (Some(executor), false) = (&self.executor, act.old_build_ref.is_empty()) else {
            return;
        };
        if let Err(e) = executor
            .cancel(&Ref {
                id: act.old_build_ref.clone(),
            })
            .await
        {
            warn!("shell: cancel superseded build {}: {e}", act.old_build_ref);
        }
    }

    /// Marks `old` superseded by `new` through the execution aggregate, tells live
    /// pages, and closes the old check run in the background.
    pub(crate) async fn supersede_execution(self: &Arc<Self>, old: &str, new: &str) {
        if let Err(e) = self
            .handle_exec(
                old,
                ReportSupersede {
                    by: new.to_string(),
                }
                .into(),
            )
            .await
        {
            warn!("supersede {old} -> {new}: {e}");
            return;
        }
        self.hub
            .publish(&format!("exec:{old}"), &format!("superseded:{new}"));
        let app = Arc::clone(self);
        let (old, new) = (old.to_string(), new.to_string());
        tokio::spawn(async move { app.close_superseded_check_run(&old, &new).await });
    }

    async fn close_superseded_check_run(&self, id: &str, superseded_by: &str) {
        let id_owned = id.to_string();
        let Ok(Some(e)) = self.db(move |s| s.get_execution(&id_owned)).await else {
            return;
        };
        let Some(cr) = e.check_run_id.filter(|c| *c != 0) else {
            return;
        };
        let upd = CheckRunUpdate {
            title: "Superseded".into(),
            summary: format!("This execution was superseded by a newer run: {superseded_by}"),
            conclusion: "neutral".into(),
            ..CheckRunUpdate::default()
        };
        if let Err(err) = self.gh.update_check_run(&e.repo, cr, &upd).await {
            warn!("close superseded check run {id}: {err}");
        }
    }

    /// The folded gate of (pr, env); reads take no lock.
    pub(crate) async fn load_gate(&self, pr: i64, env: &str) -> Result<GateState> {
        let env = env.to_string();
        Ok(self.db(move |s| s.load_gate(pr, &env)).await?.0.gate)
    }
}

#[cfg(test)]
mod tests;
