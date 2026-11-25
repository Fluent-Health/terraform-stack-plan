//! Operator surgery (Go: `adminhandlers.go`): each verb appends an audited admin
//! event to the gate stream directly, then nudges a reconcile in the background.

use std::sync::Arc;

use axum::http::StatusCode;
use axum::response::Response;
use serde::Deserialize;
use tfstackplan_gate::approval::{GrantState, Request};
use tfstackplan_gate::reconcile::{
    self, AdminCheckOverridden, AdminExecutionCancelled, AdminGateSatisfied, AdminGrantReleased,
    Event, Gate, GrantObserved, RunCompleted,
};
use tfstackplan_store::exec_stream_id;

use crate::gojson::{self, shape};
use crate::{App, http};

#[derive(Deserialize, Default)]
#[serde(default)]
struct Req {
    class: String,
    environment: String,
    pr: i64,
    reason: String,
    target: String,
    id: String,
    check: String,
    conclusion: String,
}

fn decode(body: &[u8], shape: &shape::Shape) -> Option<Req> {
    // Go answers any decode failure here with a bare "bad request".
    gojson::decode(body, shape).ok()
}

impl App {
    /// Appends `evs` to (pr, env)'s gate stream at its current version.
    async fn append_admin(&self, pr: i64, env: &str, evs: Vec<Event>) -> Result<(), &'static str> {
        let env = env.to_string();
        self.db(move |s| {
            let (mut state, ver) = s.load_gate(pr, &env)?;
            state.pr = pr;
            state.environment = env.clone();
            let state = evs.iter().fold(state, reconcile::evolve);
            s.append::<Gate>(&exec_stream_id(pr, &env), ver, &evs, &state)
        })
        .await
        .map_err(|_store| "append event")
    }

    /// Runs a tick for (pr, env) in the background.
    pub(crate) fn reconcile_background(self: &Arc<Self>, pr: i64, env: &str) {
        let app = Arc::clone(self);
        let env = env.to_string();
        tokio::spawn(async move {
            let _ = app.tick(pr, &env).await;
        });
    }
}

fn finish(app: &Arc<App>, pr: i64, env: &str, res: Result<(), &'static str>) -> Response {
    if let Err(msg) = res {
        return http::error(StatusCode::INTERNAL_SERVER_ERROR, msg);
    }
    app.reconcile_background(pr, env);
    http::ok()
}

/// POST /api/admin/grants/release.
pub(crate) async fn grants_release(app: Arc<App>, actor: String, body: &[u8]) -> Response {
    let Some(req) = decode(body, &shape::ADMIN_GRANT) else {
        return http::error(StatusCode::BAD_REQUEST, "bad request");
    };
    let ev = AdminGrantReleased {
        pr: req.pr,
        environment: req.environment.clone(),
        class: req.class.clone(),
        target: req.target.clone(),
        actor,
        reason: req.reason.clone(),
    };
    let res = app
        .append_admin(req.pr, &req.environment, vec![ev.into()])
        .await;
    if res.is_ok()
        && let Some(approval) = &app.approval
    {
        let _ = approval
            .revoke(&Request {
                class: req.class.clone(),
                target: req.target.clone(),
                pr: req.pr,
                environment: req.environment.clone(),
                requester: String::new(),
            })
            .await;
    }
    finish(&app, req.pr, &req.environment, res)
}

/// POST /api/admin/executions/cancel.
pub(crate) async fn executions_cancel(app: Arc<App>, actor: String, body: &[u8]) -> Response {
    let Some(req) = decode(body, &shape::ADMIN_CANCEL) else {
        return http::error(StatusCode::BAD_REQUEST, "bad request");
    };
    let id = req.id.clone();
    let Ok(Some(exec)) = app.db(move |s| s.get_execution(&id)).await else {
        return http::error(StatusCode::BAD_REQUEST, "get execution");
    };
    let evs = vec![
        AdminExecutionCancelled {
            kind: exec.status_context.clone(),
            execution_id: req.id.clone(),
            actor,
            reason: req.reason.clone(),
        }
        .into(),
        RunCompleted {
            kind: exec.phase.clone(),
            execution_id: req.id.clone(),
        }
        .into(),
    ];
    let res = app.append_admin(exec.pr, &exec.environment, evs).await;
    finish(&app, exec.pr, &exec.environment, res)
}

/// POST /api/admin/gates/satisfy.
pub(crate) async fn gates_satisfy(app: Arc<App>, actor: String, body: &[u8]) -> Response {
    let Some(req) = decode(body, &shape::ADMIN_SATISFY) else {
        return http::error(StatusCode::BAD_REQUEST, "bad request");
    };
    let evs = vec![
        AdminGateSatisfied {
            pr: req.pr,
            environment: req.environment.clone(),
            class: req.class.clone(),
            target: req.target.clone(),
            actor: actor.clone(),
            reason: req.reason.clone(),
        }
        .into(),
        GrantObserved {
            class: req.class.clone(),
            target: req.target.clone(),
            name: "admin-override".into(),
            state: GrantState::Active,
            requester: actor,
        }
        .into(),
    ];
    let res = app.append_admin(req.pr, &req.environment, evs).await;
    finish(&app, req.pr, &req.environment, res)
}

/// POST /api/admin/checks/override.
pub(crate) async fn checks_override(app: Arc<App>, actor: String, body: &[u8]) -> Response {
    let Some(req) = decode(body, &shape::ADMIN_OVERRIDE) else {
        return http::error(StatusCode::BAD_REQUEST, "bad request");
    };
    let ev = AdminCheckOverridden {
        pr: req.pr,
        env: req.environment.clone(),
        check_name: req.check.clone(),
        conclusion: req.conclusion.clone(),
        actor,
        reason: req.reason.clone(),
    };
    let res = app
        .append_admin(req.pr, &req.environment, vec![ev.into()])
        .await;
    finish(&app, req.pr, &req.environment, res)
}
