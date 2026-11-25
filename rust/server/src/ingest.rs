//! The runner's ingest endpoints and the gate and claim verbs (Go: `handlers.go`,
//! `gate.go`): init, phase, update, finalize, gate check, revoke and reconcile,
//! claims list and release.

use std::sync::Arc;

use axum::http::StatusCode;
use axum::response::Response;
use serde::Deserialize;
use tfstackplan_gate::events::{
    Claim, Finalize, GateCheck, GateRevoke, Init, PhaseEvent, Status, Update,
};
use tfstackplan_gate::execution::{
    ReportAnnotate, ReportFail, ReportInit, ReportPhase, ReportSucceed, ReportTick,
};
use tfstackplan_gate::reconcile::{ApplySucceeded, GateState, RunnerFinalize};
use tracing::warn;

use crate::gojson::shape;
use crate::render::{is_apply_context, is_gate, status_context};
use crate::routes::Ctx;
use crate::shell::exec_init_from_events;
use crate::{App, http};

macro_rules! decode_or_400 {
    ($body:expr, $shape:expr) => {
        match $crate::gojson::decode($body, &$shape) {
            Ok(v) => v,
            Err(e) => return $crate::http::bad_request(&e),
        }
    };
}
pub(crate) use decode_or_400;

pub(crate) async fn init(app: Arc<App>, ctx: Ctx, body: &[u8]) -> Response {
    let mut init: Init = decode_or_400!(body, shape::INIT);
    if is_gate(&init.context, &init.environment) {
        init.context = status_context(&init.environment);
    }
    if init.pr <= 0 && !init.sha.is_empty() {
        let (env, c, sha) = (
            init.environment.clone(),
            init.context.clone(),
            init.sha.clone(),
        );
        if let Ok(Some(pr)) = app
            .db(move |s| {
                Ok(match s.find_execution_by_sha(&env, &c, &sha)? {
                    Some(id) => s.get_execution(&id)?.map(|e| e.pr).filter(|pr| *pr > 0),
                    None => None,
                })
            })
            .await
        {
            init.pr = pr;
        }
    }
    if app
        .handle_exec(
            &init.id,
            ReportInit {
                exec: exec_init_from_events(&init),
            }
            .into(),
        )
        .await
        .is_err()
    {
        return http::error(StatusCode::INTERNAL_SERVER_ERROR, "store init");
    }
    if init.pr > 0 {
        let i = init.clone();
        let found = app
            .db(move |s| {
                let Some(old) = s.find_non_superseded_execution(
                    i.pr,
                    &i.environment,
                    &i.sha,
                    &i.context,
                    &i.id,
                )?
                else {
                    return Ok(None);
                };
                let created = (s.get_execution(&old)?, s.get_execution(&i.id)?);
                Ok(Some((old, created)))
            })
            .await;
        if let Ok(Some((old, created))) = found {
            match created {
                (Some(o), Some(n)) if n.created_at < o.created_at => {
                    app.supersede_execution(&init.id, &old).await
                }
                _ => app.supersede_execution(&old, &init.id).await,
            }
        }
    }
    let ui = app.ui_url(init.pr, &init.id);
    if is_gate(&init.context, &init.environment)
        && app
            .ensure_check_run(
                &init.id,
                &init.repo,
                &init.sha,
                &app.plan_check_name(&init.environment),
                &ui,
            )
            .await
            .is_err()
    {
        return http::error(StatusCode::BAD_GATEWAY, "create check run");
    }
    if is_apply_context(&init.context) {
        if app
            .ensure_check_run(&init.id, &init.repo, &init.sha, &init.context, &ui)
            .await
            .is_err()
        {
            return http::error(StatusCode::BAD_GATEWAY, "create check run");
        }
        let (env, pr, id) = (init.environment.clone(), init.pr, init.id.clone());
        let _ = app
            .db(move |s| s.associate_claim_execution(&env, pr, &id))
            .await;
    }
    app.drive(&init.id, &ctx.base_url, false).await;
    http::ok()
}

pub(crate) async fn phase(app: Arc<App>, ctx: Ctx, body: &[u8]) -> Response {
    let p: PhaseEvent = decode_or_400!(body, shape::PHASE_EVENT);
    let sig = ReportPhase {
        id: p.id.clone(),
        phase: p.phase.clone(),
        label: p.label.clone(),
        pct: p.progress_pct,
        repo: p.repo.clone(),
        sha: p.sha.clone(),
        pr: p.pr,
        environment: p.environment.clone(),
        context: p.context.clone(),
        log_url: p.log_url.clone(),
    };
    if app.handle_exec(&p.id, sig.into()).await.is_err() {
        return http::error(StatusCode::INTERNAL_SERVER_ERROR, "store phase");
    }
    let id = p.id.clone();
    let Ok(Some(e)) = app.db(move |s| s.get_execution(&id)).await else {
        return http::error(StatusCode::INTERNAL_SERVER_ERROR, "read execution");
    };
    let ui = app.ui_url(e.pr, &e.id);
    if is_gate(&e.status_context, &e.environment)
        && app
            .ensure_check_run(
                &e.id,
                &e.repo,
                &e.sha,
                &app.plan_check_name(&e.environment),
                &ui,
            )
            .await
            .is_err()
    {
        return http::error(StatusCode::BAD_GATEWAY, "create check run");
    }
    if is_apply_context(&e.status_context) {
        if app
            .ensure_check_run(&e.id, &e.repo, &e.sha, &e.status_context, &ui)
            .await
            .is_err()
        {
            return http::error(StatusCode::BAD_GATEWAY, "create check run");
        }
        app.renew_apply_claims(&e.environment, e.pr).await;
    }
    app.drive(&p.id, &ctx.base_url, false).await;
    http::ok()
}

pub(crate) async fn update(app: Arc<App>, ctx: Ctx, body: &[u8]) -> Response {
    let u: Update = decode_or_400!(body, shape::UPDATE);
    let tick = ReportTick {
        stack: u.stack.clone(),
        status: u.status,
        detail: u.detail.clone(),
    };
    if app.handle_exec(&u.id, tick.into()).await.is_err() {
        return http::error(StatusCode::INTERNAL_SERVER_ERROR, "update stack");
    }
    if app.objects.is_some() && crate::render::done(u.status) {
        let _ = app.offload_log(&u.id, &u.stack).await;
    }
    let id = u.id.clone();
    if let Ok(Some(e)) = app.db(move |s| s.get_execution(&id)).await
        && is_apply_context(&e.status_context)
    {
        app.renew_apply_claims(&e.environment, e.pr).await;
    }
    app.drive(&u.id, &ctx.base_url, false).await;
    http::ok()
}

pub(crate) async fn finalize(app: Arc<App>, ctx: Ctx, body: &[u8]) -> Response {
    let f: Finalize = decode_or_400!(body, shape::FINALIZE);
    let (id, md) = (f.id.clone(), f.report_markdown.clone());
    if app.db(move |s| s.set_report(&id, &md)).await.is_err() {
        return http::error(StatusCode::INTERNAL_SERVER_ERROR, "store report");
    }
    let (id, reasons) = (f.id.clone(), f.change_reasons.clone());
    if app
        .db(move |s| s.set_change_reasons(&id, &reasons))
        .await
        .is_err()
    {
        return http::error(StatusCode::INTERNAL_SERVER_ERROR, "store change reasons");
    }
    // Go ranges over the map in random order; the writes are independent.
    for (path, md) in &f.stack_reports {
        let (id, path, md) = (f.id.clone(), path.clone(), md.clone());
        if app
            .db(move |s| s.upsert_stack_output(&id, &path, "plan", "", &md))
            .await
            .is_err()
        {
            return http::error(StatusCode::INTERNAL_SERVER_ERROR, "store stack plan");
        }
    }
    let id = f.id.clone();
    let Ok(Some(e)) = app.db(move |s| s.get_execution(&id)).await else {
        return http::error(StatusCode::INTERNAL_SERVER_ERROR, "read execution");
    };
    let apply = is_apply_context(&e.status_context);
    if f.failed {
        if app.handle_exec(&f.id, ReportFail {}.into()).await.is_err() {
            return http::error(StatusCode::INTERNAL_SERVER_ERROR, "reconcile fail");
        }
        app.drive(&f.id, &ctx.base_url, true).await;
        app.finalize_logs_for(&f.id).await;
        return http::ok();
    }
    let annotate = ReportAnnotate {
        projects: f.projects.clone(),
        categories: f.categories.clone(),
        counts: f.counts.clone(),
        moving: f.moving.clone(),
    };
    if app.handle_exec(&f.id, annotate.into()).await.is_err() {
        return http::error(StatusCode::INTERNAL_SERVER_ERROR, "annotate stacks");
    }
    if !apply
        && app
            .handle_exec(&f.id, ReportSucceed {}.into())
            .await
            .is_err()
    {
        return http::error(StatusCode::INTERNAL_SERVER_ERROR, "reconcile succeed");
    }
    let sig = RunnerFinalize {
        gates: f.gates.clone(),
        apply_context: apply,
    };
    if app
        .handle(e.pr, &e.environment, &e.repo, sig.into())
        .await
        .is_err()
    {
        return http::error(StatusCode::INTERNAL_SERVER_ERROR, "reconcile finalize");
    }
    if apply && e.phase == "report" {
        if conclude_applied_stacks(&app, &f.id).await.is_err() {
            return http::error(StatusCode::INTERNAL_SERVER_ERROR, "conclude applied stacks");
        }
        app.drive(&f.id, &ctx.base_url, true).await;
    }
    app.finalize_logs_for(&f.id).await;
    if !apply && e.pr > 0 && !app.run_trigger_armed() {
        app.post_plan_apply_lock(&e).await;
    }
    http::ok()
}

/// Ticks every still-running stack of a successfully applied execution to safe.
async fn conclude_applied_stacks(app: &Arc<App>, exec_id: &str) -> crate::Result<()> {
    let id = exec_id.to_string();
    let g = app.db(move |s| s.load_graph(&id)).await?;
    for st in g.stacks {
        if matches!(
            st.status,
            Status::Pending | Status::Running | Status::Initializing | Status::Initialized
        ) {
            app.handle_exec(
                exec_id,
                ReportTick {
                    stack: st.path,
                    status: Status::Safe,
                    detail: "applied; its own completion tick never arrived, concluded by the apply finalize".into(),
                }
                .into(),
            )
            .await?;
        }
    }
    Ok(())
}

#[derive(Deserialize)]
struct ClaimsListReq {
    #[serde(default)]
    environment: String,
}

pub(crate) async fn claims_list(app: Arc<App>, body: &[u8]) -> Response {
    let req: ClaimsListReq = decode_or_400!(body, shape::CLAIMS_LIST);
    let Ok(claims) = app.db(move |s| s.list_claims(&req.environment)).await else {
        return http::error(StatusCode::INTERNAL_SERVER_ERROR, "list claims");
    };
    let out: Vec<Claim> = claims;
    http::json_ok(&out)
}

#[derive(Deserialize)]
struct ClaimsReleaseReq {
    #[serde(default)]
    environment: String,
    #[serde(default)]
    pr: i64,
    #[serde(default)]
    stack: String,
}

pub(crate) async fn claims_release(app: Arc<App>, body: &[u8]) -> Response {
    let req: ClaimsReleaseReq = decode_or_400!(body, shape::CLAIMS_RELEASE);
    app.admin_release_claims(&req.environment, req.pr, &req.stack)
        .await;
    http::ok()
}

#[derive(serde::Serialize)]
struct GateOk<'a> {
    requester: &'a str,
}

/// The apply-time, fail-closed gate pre-check.
pub(crate) async fn gate_check(app: Arc<App>, body: &[u8]) -> Response {
    let p: GateCheck = decode_or_400!(body, shape::GATE_CHECK);
    if app.tick(p.pr, &p.environment).await.is_err() {
        return http::coded_error(
            StatusCode::SERVICE_UNAVAILABLE,
            "GATE-003",
            "gate state could not be confirmed",
        );
    }
    let gate = match app.load_gate(p.pr, &p.environment).await {
        Ok(g) => g,
        Err(_) => {
            return http::coded_error(
                StatusCode::INTERNAL_SERVER_ERROR,
                "SRV-001",
                "load gate failed",
            );
        }
    };
    match gate {
        GateState::NotClassified => {
            http::coded_error(StatusCode::CONFLICT, "GATE-001", "not classified")
        }
        GateState::Clean => http::json_ok(&GateOk { requester: "" }),
        GateState::Satisfied(s) => http::json_ok(&GateOk {
            requester: &s.lease.requester,
        }),
        GateState::Pending(_) | GateState::Blocked(_) => {
            http::coded_error(StatusCode::CONFLICT, "GATE-002", "gate not satisfied")
        }
    }
}

pub(crate) async fn gate_revoke(app: Arc<App>, body: &[u8]) -> Response {
    let p: GateRevoke = decode_or_400!(body, shape::GATE_REVOKE);
    if let Err(e) = app
        .handle(p.pr, &p.environment, "", ApplySucceeded {}.into())
        .await
    {
        warn!(
            "reconcile-core: gate revoke pr={} env={}: {e}",
            p.pr, p.environment
        );
    }
    http::ok()
}

/// Nudges a reconcile of every pending gate in the background.
pub(crate) fn gate_reconcile(app: Arc<App>) -> Response {
    tokio::spawn(async move { app.reconcile_pending().await });
    http::status(StatusCode::ACCEPTED)
}

#[cfg(test)]
mod tests;
