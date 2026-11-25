//! POST /github/webhook (Go: `github_webhook.go`): every delivery is HMAC-checked
//! (constant time), then pull_request, push, check_run, check_suite and
//! merge_group events drive the merge lock, PR metadata and run triggering.

use std::sync::Arc;

use axum::http::{HeaderMap, StatusCode};
use axum::response::Response;
use serde::Deserialize;
use tfstackplan_gate::reconcile::{RUN_KIND_APPLY, RUN_KIND_PLAN, RunRequested};
use tfstackplan_server_clients::github::verify_github_signature;
use tfstackplan_store::PrMeta;
use tracing::{info, warn};

use crate::render::{check_run_name, run_context};
use crate::{App, http};

/// Go's `prFromMergeSubject`: squash `"<title> (#N)"` anchored at the end, or merge
/// "Merge pull request #N from …"; 0 when neither matches.
pub(crate) fn pr_from_merge_subject(subject: &str) -> i64 {
    // `\(#([0-9]+)\)\s*$`
    let trimmed = subject.trim_end_matches([' ', '\t', '\n', '\r', '\x0c']);
    if let Some(inner) = trimmed.strip_suffix(')')
        && let Some(open) = inner.rfind("(#")
    {
        let digits = &inner[open + 2..];
        if !digits.is_empty() && digits.bytes().all(|b| b.is_ascii_digit()) {
            return digits.parse().unwrap_or(0);
        }
    }
    // `^Merge pull request #([0-9]+) `
    if let Some(rest) = subject.strip_prefix("Merge pull request #") {
        let digits: String = rest.chars().take_while(char::is_ascii_digit).collect();
        if !digits.is_empty() && rest[digits.len()..].starts_with(' ') {
            return digits.parse().unwrap_or(0);
        }
    }
    0
}

#[derive(Deserialize, Default)]
#[serde(default)]
struct Repo {
    full_name: String,
}

/// Go's `json.Unmarshal` into a payload struct: a null leaves the field at its
/// zero value, so nulls are dropped before serde reads the value.
fn parse<T: for<'de> Deserialize<'de>>(body: &[u8]) -> Option<T> {
    fn strip(v: &mut serde_json::Value) {
        match v {
            serde_json::Value::Object(m) => {
                m.retain(|_, x| !x.is_null());
                m.values_mut().for_each(strip);
            }
            serde_json::Value::Array(a) => a.iter_mut().for_each(strip),
            _ => {}
        }
    }
    let mut v: serde_json::Value = serde_json::from_slice(body).ok()?;
    strip(&mut v);
    serde_json::from_value(v).ok()
}

pub(crate) async fn handle(app: Arc<App>, headers: &HeaderMap, body: &[u8]) -> Response {
    if app.cfg.github_webhook_secret.is_empty() {
        return http::error(StatusCode::NOT_FOUND, "not found");
    }
    let sig = headers
        .get("X-Hub-Signature-256")
        .and_then(|v| v.to_str().ok())
        .unwrap_or_default();
    if !verify_github_signature(app.cfg.github_webhook_secret.as_bytes(), sig, body) {
        return http::error(StatusCode::UNAUTHORIZED, "invalid signature");
    }
    let event = headers
        .get("X-GitHub-Event")
        .and_then(|v| v.to_str().ok())
        .unwrap_or_default();
    match event {
        "push" => push(app, body).await,
        "check_run" => check_run(app, body).await,
        "check_suite" => check_suite(app, body).await,
        "merge_group" => merge_group(app, body).await,
        "pull_request" => pull_request(app, body).await,
        _ => http::status(StatusCode::NO_CONTENT),
    }
}

async fn merge_group(app: Arc<App>, body: &[u8]) -> Response {
    #[derive(Deserialize, Default)]
    #[serde(default)]
    struct Mg {
        head_sha: String,
        head_ref: String,
    }
    #[derive(Deserialize, Default)]
    #[serde(default)]
    struct P {
        action: String,
        merge_group: Mg,
        repository: Repo,
    }
    let Some(p) = parse::<P>(body) else {
        return http::error(StatusCode::BAD_REQUEST, "invalid payload");
    };
    if app
        .handle_merge_group(
            &p.repository.full_name,
            &p.merge_group.head_sha,
            &p.merge_group.head_ref,
            &p.action,
        )
        .await
        .is_err()
    {
        return http::error(StatusCode::INTERNAL_SERVER_ERROR, "merge_group eval failed");
    }
    http::status(StatusCode::NO_CONTENT)
}

async fn pull_request(app: Arc<App>, body: &[u8]) -> Response {
    #[derive(Deserialize, Default)]
    #[serde(default)]
    struct Head {
        sha: String,
        #[serde(rename = "ref")]
        r#ref: String,
    }
    #[derive(Deserialize, Default)]
    #[serde(default)]
    struct User {
        login: String,
    }
    #[derive(Deserialize, Default)]
    #[serde(default)]
    struct Pr {
        number: i64,
        merged: bool,
        title: String,
        body: Option<String>,
        html_url: String,
        head: Head,
        user: User,
        auto_merge: Option<serde_json::Value>,
    }
    #[derive(Deserialize, Default)]
    #[serde(default)]
    struct P {
        action: String,
        pull_request: Pr,
        repository: Repo,
    }
    let Some(p) = parse::<P>(body) else {
        return http::error(StatusCode::BAD_REQUEST, "invalid payload");
    };
    let pr = p.pull_request.number;
    let repo = p.repository.full_name.clone();
    match p.action.as_str() {
        "opened" | "reopened" | "synchronize" | "edited" => {
            if pr > 0 {
                app.handle_pr_apply_lock(&repo, pr, false).await;
                let meta = PrMeta {
                    repo: repo.clone(),
                    pr,
                    title: p.pull_request.title.clone(),
                    body: p.pull_request.body.clone().unwrap_or_default(),
                    author_login: p.pull_request.user.login.clone(),
                    head_ref: p.pull_request.head.r#ref.clone(),
                    url: p.pull_request.html_url.clone(),
                    auto_merge: p
                        .pull_request
                        .auto_merge
                        .as_ref()
                        .is_some_and(|v| !v.is_null()),
                    updated_at: None,
                };
                if let Err(e) = app.db(move |s| s.upsert_pr_meta(&meta)).await {
                    warn!("webhook: upsert pr_meta pr={pr}: {e}");
                }
                if app.run_trigger_armed() {
                    let sig = RunRequested {
                        kind: RUN_KIND_PLAN.into(),
                        sha: p.pull_request.head.sha.clone(),
                        branch: p.pull_request.head.r#ref.clone(),
                        rerun: false,
                    };
                    let env = app.cfg.environment.clone();
                    if let Err(e) = app.handle(pr, &env, &repo, sig.into()).await {
                        warn!("webhook: plan run request pr={pr}: {e}");
                        return http::error(
                            StatusCode::INTERNAL_SERVER_ERROR,
                            "run request failed",
                        );
                    }
                }
            }
            http::status(StatusCode::NO_CONTENT)
        }
        "closed" => {
            if pr <= 0 {
                return http::error(StatusCode::BAD_REQUEST, "invalid PR number");
            }
            if p.pull_request.merged {
                info!("webhook: PR #{pr} merged — leaving grant for the apply");
                app.handle_pr_apply_lock(&repo, pr, true).await;
                return http::status(StatusCode::NO_CONTENT);
            }
            info!("webhook: PR #{pr} abandoned — revoking orphaned grants");
            app.revoke_orphans(pr).await;
            http::status(StatusCode::NO_CONTENT)
        }
        _ => http::status(StatusCode::NO_CONTENT),
    }
}

async fn push(app: Arc<App>, body: &[u8]) -> Response {
    if !app.run_trigger_armed() {
        return http::status(StatusCode::NO_CONTENT);
    }
    #[derive(Deserialize, Default)]
    #[serde(default)]
    struct Commit {
        message: String,
    }
    #[derive(Deserialize, Default)]
    #[serde(default)]
    struct P {
        #[serde(rename = "ref")]
        r#ref: String,
        after: String,
        head_commit: Option<Commit>,
        repository: Repo,
    }
    let Some(p) = parse::<P>(body) else {
        return http::error(StatusCode::BAD_REQUEST, "invalid payload");
    };
    if p.r#ref != "refs/heads/main" || p.after.is_empty() {
        return http::status(StatusCode::NO_CONTENT);
    }
    let message = p.head_commit.map(|c| c.message).unwrap_or_default();
    let subject = message.split('\n').next().unwrap_or_default();
    let pr = pr_from_merge_subject(subject);
    if pr == 0 {
        let short: String = p.after.chars().take(12).collect();
        info!("webhook: push {short} to main has no recoverable PR — skipping apply run");
        return http::status(StatusCode::NO_CONTENT);
    }
    let sig = RunRequested {
        kind: RUN_KIND_APPLY.into(),
        sha: p.after.clone(),
        branch: "main".into(),
        rerun: false,
    };
    let env = app.cfg.environment.clone();
    if let Err(e) = app
        .handle(pr, &env, &p.repository.full_name, sig.into())
        .await
    {
        warn!("webhook: apply run request pr={pr}: {e}");
        return http::error(StatusCode::INTERNAL_SERVER_ERROR, "run request failed");
    }
    http::status(StatusCode::NO_CONTENT)
}

async fn check_run(app: Arc<App>, body: &[u8]) -> Response {
    if !app.run_trigger_armed() {
        return http::status(StatusCode::NO_CONTENT);
    }
    #[derive(Deserialize, Default)]
    #[serde(default)]
    struct PrRef {
        number: i64,
    }
    #[derive(Deserialize, Default)]
    #[serde(default)]
    struct Cr {
        name: String,
        head_sha: String,
        pull_requests: Option<Vec<PrRef>>,
    }
    #[derive(Deserialize, Default)]
    #[serde(default)]
    struct P {
        action: String,
        check_run: Cr,
        repository: Repo,
    }
    let Some(p) = parse::<P>(body) else {
        return http::error(StatusCode::BAD_REQUEST, "invalid payload");
    };
    if p.action != "rerequested" {
        return http::status(StatusCode::NO_CONTENT);
    }
    let env = app.cfg.environment.clone();
    let name = p.check_run.name.as_str();
    let kind = if name == app.plan_check_name(&env) || name == check_run_name(&env) {
        RUN_KIND_PLAN
    } else if name == run_context(RUN_KIND_APPLY, &env) {
        RUN_KIND_APPLY
    } else {
        return http::status(StatusCode::NO_CONTENT);
    };
    let prs = p.check_run.pull_requests.unwrap_or_default();
    let pr = match prs.first() {
        Some(r) => r.number,
        None => {
            let (sha, e, ctx) = (
                p.check_run.head_sha.clone(),
                env.clone(),
                run_context(kind, &env),
            );
            app.db(move |s| Ok(s.latest_pr_for_sha(&sha, &e, &ctx)))
                .await
                .ok()
                .flatten()
                .unwrap_or(0)
        }
    };
    if pr == 0 {
        let short: String = p.check_run.head_sha.chars().take(12).collect();
        info!("webhook: check_run rerequested for {name} {short} carries no PR — skipping");
        return http::status(StatusCode::NO_CONTENT);
    }
    let sig = RunRequested {
        kind: kind.into(),
        sha: p.check_run.head_sha.clone(),
        branch: String::new(),
        rerun: true,
    };
    if let Err(e) = app
        .handle(pr, &env, &p.repository.full_name, sig.into())
        .await
    {
        warn!("webhook: rerun request pr={pr} kind={kind}: {e}");
        return http::error(StatusCode::INTERNAL_SERVER_ERROR, "run request failed");
    }
    http::status(StatusCode::NO_CONTENT)
}

async fn check_suite(app: Arc<App>, body: &[u8]) -> Response {
    if !app.run_trigger_armed() {
        return http::status(StatusCode::NO_CONTENT);
    }
    #[derive(Deserialize, Default)]
    #[serde(default)]
    struct Cs {
        head_sha: String,
    }
    #[derive(Deserialize, Default)]
    #[serde(default)]
    struct P {
        action: String,
        check_suite: Cs,
        repository: Repo,
    }
    let Some(p) = parse::<P>(body) else {
        return http::error(StatusCode::BAD_REQUEST, "invalid payload");
    };
    if p.action != "rerequested" || p.check_suite.head_sha.is_empty() {
        return http::status(StatusCode::NO_CONTENT);
    }
    let env = app.cfg.environment.clone();
    for kind in [RUN_KIND_PLAN, RUN_KIND_APPLY] {
        let (e, ctx, sha) = (
            env.clone(),
            run_context(kind, &env),
            p.check_suite.head_sha.clone(),
        );
        let found = app
            .db(move |s| {
                Ok(match s.find_execution_by_sha(&e, &ctx, &sha)? {
                    Some(id) => s.get_execution(&id)?,
                    None => None,
                })
            })
            .await;
        let Ok(Some(e)) = found else { continue };
        if e.status != "failure" || e.pr <= 0 {
            continue;
        }
        let sig = RunRequested {
            kind: kind.into(),
            sha: p.check_suite.head_sha.clone(),
            branch: String::new(),
            rerun: true,
        };
        if let Err(err) = app
            .handle(e.pr, &env, &p.repository.full_name, sig.into())
            .await
        {
            warn!("webhook: suite rerun pr={} kind={kind}: {err}", e.pr);
        }
    }
    http::status(StatusCode::NO_CONTENT)
}

#[cfg(test)]
mod tests;
