//! The background loops (Go: the loop half of `gate.go`, `applylock.go`,
//! `watchdog.go`): reconcile pending gates, sweep orphaned grants, fail runs whose
//! build never reported, and re-evaluate held checks when claims expire. Each runs
//! until its task is aborted at shutdown.

use std::sync::Arc;
use std::time::Duration;

use jiff::SignedDuration;
use tfstackplan_gate::reconcile::{PRClosed, RUN_KIND_APPLY, RUN_KIND_PLAN, RunStartResult};
use tfstackplan_server_clients::executor::{Phase, Ref};
use tracing::{info, warn};

use crate::App;
use crate::render::is_apply_context;

/// A ticker that, like Go's `time.NewTicker`, first fires after one interval.
fn ticker(every: Duration) -> tokio::time::Interval {
    let mut t = tokio::time::interval_at(tokio::time::Instant::now() + every, every);
    t.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Delay);
    t
}

impl App {
    /// Re-evaluates every gate not yet fully active.
    pub(crate) async fn reconcile_pending(self: &Arc<Self>) {
        if self.approval.is_none() {
            return;
        }
        let pending = match self.db(|s| s.pending_gates()).await {
            Ok(p) => p,
            Err(e) => {
                warn!("reconcile: list pending gates: {e}");
                return;
            }
        };
        for g in pending {
            if let Err(e) = self.tick(g.pr, &g.environment).await {
                warn!("reconcile: gate pr={} env={}: {e}", g.pr, g.environment);
            }
        }
    }

    /// The PR-closed transition for every environment the PR holds targets in.
    pub(crate) async fn revoke_orphans(self: &Arc<Self>, pr: i64) {
        let targets = match self.db(move |s| s.pr_targets(pr)).await {
            Ok(t) => t,
            Err(e) => {
                warn!("webhook: load targets for PR #{pr}: {e}");
                return;
            }
        };
        let mut envs: Vec<String> = targets.into_iter().map(|t| t.environment).collect();
        envs.sort();
        envs.dedup();
        for env in envs {
            if let Err(e) = self.handle(pr, &env, "", PRClosed {}.into()).await {
                warn!("reconcile-core: pr-closed pr={pr} env={env}: {e}");
            }
        }
    }

    /// Revokes grants left open on abandoned PRs that escaped the close webhook.
    pub(crate) async fn sweep_orphaned_grants(self: &Arc<Self>) {
        let prs = match self.db(|s| s.open_grant_prs()).await {
            Ok(p) => p,
            Err(e) => {
                warn!("sweep: list open-grant PRs: {e}");
                return;
            }
        };
        for p in prs {
            if p.repo.is_empty() {
                info!(
                    "sweep: PR #{} has open grants but no execution repo — skipping",
                    p.pr
                );
                continue;
            }
            match self.gh.pr_abandoned(&p.repo, p.pr).await {
                Err(e) => warn!("sweep: PRAbandoned(repo={} pr={}): {e}", p.repo, p.pr),
                Ok(true) => {
                    info!(
                        "sweep: PR #{} (repo={}) is abandoned but holds open grants — revoking",
                        p.pr, p.repo
                    );
                    self.revoke_orphans(p.pr).await;
                }
                Ok(false) => {}
            }
        }
    }

    /// One watchdog round: fail serve-started runs whose build is gone, failed or
    /// finished without the runner reporting.
    pub(crate) async fn watch_runs_once(self: &Arc<Self>, timeout: Duration) {
        let Some(executor) = self.executor.clone() else {
            return;
        };
        let env = self.cfg.environment.clone();
        let cutoff = self.now() - SignedDuration::try_from(timeout).unwrap_or(SignedDuration::ZERO);
        let stuck = match self
            .db(move |s| s.stuck_pending_executions(&env, cutoff))
            .await
        {
            Ok(s) => s,
            Err(e) => {
                warn!("watchdog: list stuck executions: {e}");
                return;
            }
        };
        for e in stuck {
            let kind = if is_apply_context(&e.status_context) {
                RUN_KIND_APPLY
            } else {
                RUN_KIND_PLAN
            };
            let env_o = e.environment.clone();
            let pr = e.pr;
            let world = match self.db(move |s| s.load_gate(pr, &env_o)).await {
                Ok((cs, _)) => cs,
                Err(err) => {
                    warn!("watchdog: gather pr={} env={}: {err}", e.pr, e.environment);
                    continue;
                }
            };
            let Some(r) = world.runs.get(kind) else {
                continue;
            };
            if r.execution_id != e.id || !r.live() {
                continue;
            }
            let reason = if r.build_ref.is_empty() {
                "build start never completed"
            } else {
                match executor
                    .probe(&Ref {
                        id: r.build_ref.clone(),
                    })
                    .await
                {
                    Err(err) => {
                        warn!("watchdog: probe {}: {err}", r.build_ref);
                        continue;
                    }
                    Ok(Phase::Queued | Phase::Working) => continue,
                    Ok(Phase::Failed) => "build failed before the runner reported",
                    Ok(Phase::Done) => "build finished without the runner ever reporting",
                    Ok(Phase::NotFound) => "build not found at the executor",
                }
            };
            let sig = RunStartResult {
                kind: kind.into(),
                execution_id: e.id.clone(),
                build_ref: String::new(),
                err: reason.into(),
            };
            if let Err(err) = self.handle(e.pr, &e.environment, &e.repo, sig.into()).await {
                warn!("watchdog: fail run {}: {err}", e.id);
            }
        }
    }

    /// Re-evaluates every not-yet-active gate on a timer; returns at once without
    /// an approval backend.
    pub async fn reconcile_loop(self: Arc<Self>, every: Duration) {
        if self.approval.is_none() {
            return;
        }
        let mut t = ticker(every);
        loop {
            t.tick().await;
            self.reconcile_pending().await;
        }
    }

    /// Sweeps orphaned grants on a timer; returns at once without an approval
    /// backend.
    pub async fn orphan_sweep_loop(self: Arc<Self>, every: Duration) {
        if self.approval.is_none() {
            return;
        }
        let mut t = ticker(every);
        loop {
            t.tick().await;
            self.sweep_orphaned_grants().await;
        }
    }

    /// The start watchdog; returns at once unless run triggering is armed.
    pub async fn watchdog_loop(self: Arc<Self>, every: Duration, timeout: Duration) {
        if !self.run_trigger_armed() {
            return;
        }
        let mut t = ticker(every);
        loop {
            t.tick().await;
            self.watch_runs_once(timeout).await;
        }
    }

    /// Releases expired claims and re-evaluates held checks on a timer.
    pub async fn claims_sweep_loop(self: Arc<Self>, every: Duration) {
        let mut t = ticker(every);
        loop {
            t.tick().await;
            self.sweep_claims_once().await;
        }
    }
}

#[cfg(test)]
mod tests;
