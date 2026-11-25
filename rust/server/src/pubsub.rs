//! Pub/Sub push endpoints (Go: `pubsub.go`, `cloudbuildpush.go`): OIDC-verified
//! deliveries that re-drive an execution, reconcile pending gates, or reconcile a
//! Cloud Build serve did not launch onto its run.

use std::collections::HashMap;
use std::sync::Arc;

use axum::http::{HeaderMap, StatusCode};
use axum::response::Response;
use base64::Engine as _;
use serde::Deserialize;
use tfstackplan_gate::reconcile::InboundBuild;
use tfstackplan_store::ExecutionRow;
use tracing::warn;

use crate::gojson::{self, shape};
use crate::render::run_context;
use crate::{App, http};

#[derive(Deserialize, Default)]
#[serde(default)]
struct Message {
    data: String,
    attributes: HashMap<String, String>,
}

#[derive(Deserialize, Default)]
#[serde(default)]
struct Envelope {
    message: Message,
}

/// Checks the push bearer token; `Err` is the response to send.
async fn authorize(app: &App, headers: &HeaderMap) -> Result<(), Box<Response>> {
    let Some(verify) = &app.push_verifier else {
        return Err(Box::new(http::error(StatusCode::NOT_FOUND, "not found")));
    };
    let auth = headers
        .get("Authorization")
        .and_then(|v| v.to_str().ok())
        .unwrap_or_default();
    let tok = auth.strip_prefix("Bearer ").unwrap_or(auth);
    if tok.is_empty() || tok == auth {
        return Err(Box::new(http::error(
            StatusCode::UNAUTHORIZED,
            "missing bearer token",
        )));
    }
    let Ok(email) = verify(tok.to_string()).await else {
        return Err(Box::new(http::error(StatusCode::UNAUTHORIZED, "invalid OIDC token")));
    };
    if !app.cfg.push_service_account.is_empty() && email != app.cfg.push_service_account {
        return Err(Box::new(http::error(
            StatusCode::FORBIDDEN,
            "unauthorized push identity",
        )));
    }
    Ok(())
}

/// POST /pubsub/push.
pub(crate) async fn push(app: Arc<App>, headers: &HeaderMap, body: &[u8]) -> Response {
    if let Err(r) = authorize(&app, headers).await {
        return *r;
    }
    let Ok(env) = gojson::decode::<Envelope>(body, &shape::PUSH_ENVELOPE) else {
        return http::error(StatusCode::BAD_REQUEST, "bad push envelope");
    };
    let mut id = env
        .message
        .attributes
        .get("execution_id")
        .cloned()
        .unwrap_or_default();
    if id.is_empty()
        && !env.message.data.is_empty()
        && let Ok(raw) = base64::engine::general_purpose::STANDARD.decode(&env.message.data)
    {
        #[derive(Deserialize, Default)]
        #[serde(default)]
        struct P {
            id: String,
        }
        id = serde_json::from_slice::<P>(&raw)
            .map(|p| p.id)
            .unwrap_or_default();
    }
    if id.is_empty() {
        app.reconcile_pending().await;
    } else {
        let base = app.cfg.public_base_url.trim_end_matches('/').to_string();
        app.drive(&id, &base, true).await;
    }
    http::status(StatusCode::NO_CONTENT)
}

#[derive(Deserialize, Default)]
#[serde(default, rename_all = "camelCase")]
struct RepoSource {
    commit_sha: String,
}

#[derive(Deserialize, Default)]
#[serde(default, rename_all = "camelCase")]
struct Provenance {
    resolved_repo_source: RepoSource,
}

#[derive(Deserialize, Default)]
#[serde(default, rename_all = "camelCase")]
struct CloudBuild {
    id: String,
    substitutions: HashMap<String, String>,
    source_provenance: Provenance,
}

/// POST /pubsub/cloud-builds. Always acknowledges after auth: a wedged
/// subscription is worse than a dropped event.
pub(crate) async fn cloud_builds(app: Arc<App>, headers: &HeaderMap, body: &[u8]) -> Response {
    if app.push_verifier.is_none()
        || !app.run_trigger_armed()
        || app.cfg.build_trigger_names.is_empty()
    {
        return http::error(StatusCode::NOT_FOUND, "not found");
    }
    if let Err(r) = authorize(&app, headers).await {
        return *r;
    }
    let env = match gojson::decode::<Envelope>(body, &shape::PUSH_ENVELOPE) {
        Ok(env) => env,
        Err(e) => {
            warn!("cloud-build push: bad envelope: {e}");
            return http::status(StatusCode::NO_CONTENT);
        }
    };
    if let Ok(raw) = base64::engine::general_purpose::STANDARD.decode(&env.message.data)
        && let Ok(b) = serde_json::from_slice::<CloudBuild>(&raw)
        && !b.id.is_empty()
    {
        app.reconcile_inbound_build(b).await;
    }
    http::status(StatusCode::NO_CONTENT)
}

impl App {
    async fn reconcile_inbound_build(self: &Arc<Self>, b: CloudBuild) {
        let sub = |k: &str| b.substitutions.get(k).cloned().unwrap_or_default();
        let Some(kind) = self
            .cfg
            .build_trigger_names
            .get(&sub("TRIGGER_NAME"))
            .cloned()
        else {
            return;
        };
        if kind.is_empty() {
            return;
        }
        let mut sha = sub("COMMIT_SHA");
        if sha.is_empty() {
            sha = b.source_provenance.resolved_repo_source.commit_sha.clone();
        }
        if sha.is_empty() {
            return;
        }
        let env = self.cfg.environment.clone();
        let ctx = run_context(&kind, &env);
        let (exec_id, pr_number) = (
            sub("_EXECUTION_ID"),
            sub("_PR_NUMBER").parse::<i64>().unwrap_or(0),
        );
        let (e, sha_o) = (env.clone(), sha.clone());
        let owner = self
            .db(move |s| {
                let mut owner: Option<ExecutionRow> = None;
                if !exec_id.is_empty() {
                    owner = s.get_execution(&exec_id)?;
                }
                if owner.as_ref().is_none_or(|o| o.pr == 0)
                    && pr_number > 0
                    && let Some(id) = s.latest_execution_id(pr_number, &e)?
                {
                    owner = s.get_execution(&id)?;
                }
                if owner.as_ref().is_none_or(|o| o.pr == 0)
                    && let Some(id) = s.find_execution_by_sha(&e, &ctx, &sha_o)?
                {
                    owner = s.get_execution(&id)?;
                }
                Ok(owner)
            })
            .await
            .ok()
            .flatten();
        let Some(owner) = owner.filter(|o| o.pr != 0) else {
            return;
        };
        let sig = InboundBuild {
            kind,
            sha,
            build_ref: b.id.clone(),
        };
        if let Err(e) = self.handle(owner.pr, &env, &owner.repo, sig.into()).await {
            warn!(
                "cloud-build push: reconcile pr={} build={}: {e}",
                owner.pr, b.id
            );
        }
    }
}
