//! The merge lock (Go: `applylock.go`): evaluating a PR's stacks against the
//! environment's claim ledger, the apply-lock check runs, merge groups, and the
//! claim commands the handlers issue.

use std::collections::BTreeMap;
use std::sync::Arc;

use jiff::Timestamp;
use tfstackplan_gate::claims::{AcquireClaim, ReleaseClaim, ReleaseClaimStack, RenewClaim, held};
use tfstackplan_server_clients::github::CheckRunUpdate;
use tfstackplan_store::{ApplyLockCheck, ExecutionRow};
use tracing::warn;

use crate::render::{ApplyLockVerdict, apply_lock_output};
use crate::{App, Error, Result};

/// Go's `prFromMergeQueueRef`: the PR of a `gh-readonly-queue/<base>/pr-<n>-<sha>` ref.
pub(crate) fn pr_from_merge_queue_ref(r: &str) -> i64 {
    // Go: regexp `gh-readonly-queue/.+/pr-([0-9]+)-`, leftmost match, greedy `.+`.
    let Some(start) = r.find("gh-readonly-queue/") else {
        return 0;
    };
    let rest = &r[start + "gh-readonly-queue/".len()..];
    // Greedy `.+` takes the last "/pr-<digits>-" with at least one char before it.
    let mut best = 0;
    let mut search = 1;
    while let Some(off) = rest.get(search..).and_then(|s| s.find("/pr-")) {
        let at = search + off;
        let digits: String = rest[at + 4..]
            .chars()
            .take_while(char::is_ascii_digit)
            .collect();
        if !digits.is_empty() && rest[at + 4 + digits.len()..].starts_with('-') {
            best = digits.parse().unwrap_or(0);
        }
        search = at + 1;
    }
    best
}

impl App {
    /// A PR's changed stacks in `env` from its latest reported execution; `None`
    /// when there is no plan to read (callers fail closed).
    pub(crate) async fn pr_changed_stacks(&self, env: &str, pr: i64) -> Option<Vec<String>> {
        let env = env.to_string();
        self.db(move |s| {
            let Some(id) = s.latest_reported_execution_id(pr, &env) else {
                return Ok(None);
            };
            Ok(s.load_graph(&id)
                .ok()
                .map(|g| g.stacks.into_iter().map(|s| s.path).collect()))
        })
        .await
        .ok()
        .flatten()
    }

    /// The verdict for `pr`'s stacks in `env` at `now`, folded from the claim ledger.
    pub(crate) async fn eval_apply_lock(
        &self,
        env: &str,
        pr: i64,
        stacks: &[String],
        now: Timestamp,
    ) -> ApplyLockVerdict {
        let env = env.to_string();
        let Ok(cs) = self.db(move |s| s.load_claims(&env)).await else {
            return ApplyLockVerdict {
                state: "unverifiable".into(),
                reason: "claim-ledger load failed".into(),
                ..ApplyLockVerdict::default()
            };
        };
        let v = held(&cs, pr, stacks, now);
        if !v.held {
            return ApplyLockVerdict {
                state: "clear".into(),
                ..ApplyLockVerdict::default()
            };
        }
        ApplyLockVerdict {
            state: "held".into(),
            blocking: v.blocking,
            blocking_prs: v.blocking_prs,
            reason: "stacks being applied by another PR".into(),
        }
    }

    fn apply_lock_details_url(&self, pr: i64) -> String {
        if self.cfg.ui_base_url.is_empty() {
            return String::new();
        }
        format!("{}/pr/{pr}", self.cfg.ui_base_url)
    }

    /// Creates or updates the apply-lock check on `sha` and records it.
    #[expect(
        clippy::too_many_arguments,
        reason = "Go's postApplyLock signature, kept for side-by-side reading"
    )]
    pub(crate) async fn post_apply_lock(
        &self,
        repo: &str,
        env: &str,
        sha: &str,
        pr: i64,
        stacks: Option<Vec<String>>,
        kind: &str,
        v: &ApplyLockVerdict,
    ) -> Result<()> {
        let (env_o, sha_o) = (env.to_string(), sha.to_string());
        let rec = self
            .db(move |s| s.get_apply_lock_check(&env_o, &sha_o))
            .await?;
        let cr = match rec {
            Some(r) => r.check_run_id,
            None => self
                .gh
                .create_check_run(
                    repo,
                    sha,
                    &self.merge_gate_check_name(env),
                    &self.apply_lock_details_url(pr),
                )
                .await
                .map_err(|e| Error::Message(e.to_string()))?,
        };
        let (title, summary, conclusion) = apply_lock_output(v);
        self.gh
            .update_check_run(
                repo,
                cr,
                &CheckRunUpdate {
                    title,
                    summary,
                    details_url: self.apply_lock_details_url(pr),
                    conclusion,
                    ..CheckRunUpdate::default()
                },
            )
            .await
            .map_err(|e| Error::Message(e.to_string()))?;
        let rec = ApplyLockCheck {
            environment: env.into(),
            head_sha: sha.into(),
            check_run_id: cr,
            pr,
            repo: repo.into(),
            stacks,
            state: v.state.clone(),
            kind: kind.into(),
            execution_id: String::new(),
        };
        self.db(move |s| s.upsert_apply_lock_check(&rec)).await
    }

    async fn post_apply_lock_unverifiable(
        &self,
        repo: &str,
        env: &str,
        sha: &str,
        pr: i64,
        kind: &str,
        reason: String,
    ) {
        let v = ApplyLockVerdict {
            state: "unverifiable".into(),
            reason,
            ..ApplyLockVerdict::default()
        };
        let _ = self
            .post_apply_lock(repo, env, sha, pr, None, kind, &v)
            .await;
    }

    async fn environments_for_pr(&self, pr: i64) -> Result<Vec<String>> {
        self.db(move |s| s.environments_for_pr(pr)).await
    }

    /// On PR open, sync or reopen: post the apply-lock check per environment (not on
    /// an armed tier, whose consolidated check carries the lock). On merge: claim
    /// the PR's stacks.
    pub(crate) async fn handle_pr_apply_lock(&self, repo: &str, pr: i64, merged: bool) {
        let Ok(envs) = self.environments_for_pr(pr).await else {
            return;
        };
        let now = self.now();
        for env in envs {
            let stacks = self.pr_changed_stacks(&env, pr).await;
            if merged {
                if let Some(stacks) = stacks {
                    let _ = self
                        .handle_claim(&env, AcquireClaim { pr, stacks, now }.into())
                        .await;
                }
                continue;
            }
            if self.run_trigger_armed() {
                continue;
            }
            let Ok(sha) = self.gh.pr_head_sha(repo, pr).await else {
                continue;
            };
            let Some(stacks) = stacks else {
                self.post_apply_lock_unverifiable(
                    repo,
                    &env,
                    &sha,
                    pr,
                    "pr_head",
                    format!("no successful plan for #{pr}"),
                )
                .await;
                continue;
            };
            let v = self.eval_apply_lock(&env, pr, &stacks, now).await;
            let _ = self
                .post_apply_lock(repo, &env, &sha, pr, Some(stacks), "pr_head", &v)
                .await;
        }
    }

    /// The PRs of a merge group, falling back to the merge-queue head ref when the
    /// commit lookup is empty.
    async fn merge_group_prs(
        &self,
        repo: &str,
        head_sha: &str,
        head_ref: &str,
    ) -> Result<Vec<i64>> {
        let prs = self
            .gh
            .merge_group_prs(repo, head_sha)
            .await
            .map_err(|e| Error::Message(e.to_string()))?;
        if prs.is_empty() {
            let pr = pr_from_merge_queue_ref(head_ref);
            if pr > 0 {
                return Ok(vec![pr]);
            }
        }
        Ok(prs)
    }

    /// Evaluates the merge lock for a merge group; an error lets GitHub redeliver.
    pub(crate) async fn handle_merge_group(
        self: &Arc<Self>,
        repo: &str,
        head_sha: &str,
        head_ref: &str,
        action: &str,
    ) -> Result<()> {
        if action == "destroyed" {
            if let Ok(prs) = self.merge_group_prs(repo, head_sha, head_ref).await {
                for pr in prs {
                    let envs = self.environments_for_pr(pr).await.unwrap_or_default();
                    for env in envs {
                        let (env_o, sha_o) = (env.clone(), head_sha.to_string());
                        if let Ok(Some(rec)) = self
                            .db(move |s| s.get_apply_lock_check(&env_o, &sha_o))
                            .await
                        {
                            let _ = self
                                .handle_claim(&env, ReleaseClaim { pr: rec.pr }.into())
                                .await;
                            self.reevaluate_held(&env).await;
                        }
                    }
                }
            }
            return Ok(());
        }
        let prs = self.merge_group_prs(repo, head_sha, head_ref).await?;
        let mut env_pr_stacks: BTreeMap<String, BTreeMap<i64, Vec<String>>> = BTreeMap::new();
        for &pr in &prs {
            let envs = self.environments_for_pr(pr).await?;
            for env in envs {
                let Some(ps) = self.pr_changed_stacks(&env, pr).await else {
                    self.post_apply_lock_unverifiable(
                        repo,
                        &env,
                        head_sha,
                        pr,
                        "merge_group",
                        format!("no successful plan for #{pr}"),
                    )
                    .await;
                    return Ok(());
                };
                env_pr_stacks.entry(env).or_default().insert(pr, ps);
            }
        }
        if !self.cfg.environment.is_empty() {
            env_pr_stacks
                .entry(self.cfg.environment.clone())
                .or_default();
        }
        let now = self.now();
        for (env, pr_stacks) in env_pr_stacks {
            // Go appends into a nil slice: no stacks at all stays nil.
            let mut all: Option<Vec<String>> = None;
            let mut owner = 0;
            for (pr, ss) in pr_stacks {
                if !ss.is_empty() {
                    all.get_or_insert_with(Vec::new).extend(ss);
                }
                owner = pr;
            }
            if owner == 0
                && let Some(&first) = prs.first()
            {
                owner = first;
            }
            let flat = all.clone().unwrap_or_default();
            let v = self.eval_apply_lock(&env, owner, &flat, now).await;
            if v.state == "clear" {
                let _ = self
                    .handle_claim(
                        &env,
                        AcquireClaim {
                            pr: owner,
                            stacks: flat,
                            now,
                        }
                        .into(),
                    )
                    .await;
            }
            let _ = self
                .post_apply_lock(repo, &env, head_sha, owner, all, "merge_group", &v)
                .await;
        }
        Ok(())
    }

    /// Re-evaluates held checks for every environment that had an expired claim.
    pub(crate) async fn sweep_claims_once(self: &Arc<Self>) {
        let now = self.now();
        let Ok(envs) = self.db(move |s| s.sweep_expired_claims(now)).await else {
            return;
        };
        for env in envs {
            self.reevaluate_held(&env).await;
            let env_o = env.clone();
            let _ = self.db(move |s| s.rebuild_claims(&env_o)).await;
        }
    }

    /// The admin release: one stack, or all of a PR's claims, then re-evaluation.
    pub(crate) async fn admin_release_claims(self: &Arc<Self>, env: &str, pr: i64, stack: &str) {
        let cmd = if stack.is_empty() {
            ReleaseClaim { pr }.into()
        } else {
            ReleaseClaimStack {
                pr,
                stack: stack.into(),
            }
            .into()
        };
        let _ = self.handle_claim(env, cmd).await;
        self.reevaluate_held(env).await;
    }

    /// Posts the apply-lock check right after a plan finalizes.
    pub(crate) async fn post_plan_apply_lock(&self, e: &ExecutionRow) {
        let id = e.id.clone();
        let Ok(g) = self.db(move |s| s.load_graph(&id)).await else {
            return;
        };
        let stacks: Vec<String> = g.stacks.into_iter().map(|s| s.path).collect();
        let v = self
            .eval_apply_lock(&e.environment, e.pr, &stacks, self.now())
            .await;
        let _ = self
            .post_apply_lock(
                &e.repo,
                &e.environment,
                &e.sha,
                e.pr,
                Some(stacks),
                "pr_head",
                &v,
            )
            .await;
    }

    /// Drops a PR's claims in `env` and re-evaluates held checks.
    pub(crate) async fn release_apply_claims(self: &Arc<Self>, env: &str, pr: i64) {
        let _ = self.handle_claim(env, ReleaseClaim { pr }.into()).await;
        self.reevaluate_held(env).await;
    }

    /// Extends a PR's lease in `env` (the apply heartbeat).
    pub(crate) async fn renew_apply_claims(&self, env: &str, pr: i64) {
        let _ = self
            .handle_claim(
                env,
                RenewClaim {
                    pr,
                    now: self.now(),
                }
                .into(),
            )
            .await;
    }

    /// Re-posts every held apply-lock check of `env` whose blockers may have cleared.
    pub(crate) async fn reevaluate_held(self: &Arc<Self>, env: &str) {
        let env_o = env.to_string();
        let Ok(held) = self.db(move |s| s.held_apply_lock_checks(&env_o)).await else {
            return;
        };
        let now = self.now();
        let base = self.cfg.public_base_url.trim_end_matches('/').to_string();
        for c in held {
            if !c.execution_id.is_empty() && self.run_trigger_armed() {
                // Box the recursion: drive can reach reevaluate_held again through a
                // terminal render's claim release.
                Box::pin(self.drive(&c.execution_id, &base, true)).await;
                continue;
            }
            let flat = c.stacks.clone().unwrap_or_default();
            let v = self.eval_apply_lock(env, c.pr, &flat, now).await;
            if v.state == "clear" && c.kind == "merge_group" {
                let _ = self
                    .handle_claim(
                        env,
                        AcquireClaim {
                            pr: c.pr,
                            stacks: flat,
                            now,
                        }
                        .into(),
                    )
                    .await;
            }
            if let Err(e) = self
                .post_apply_lock(
                    &c.repo,
                    env,
                    &c.head_sha,
                    c.pr,
                    c.stacks.clone(),
                    &c.kind,
                    &v,
                )
                .await
            {
                warn!("reevaluate held {env}/{}: {e}", c.head_sha);
            }
        }
    }
}

#[cfg(test)]
mod tests;
