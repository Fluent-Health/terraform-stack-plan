//! The execution read (Go: `executionhandlers.go`): the frozen PascalCase shape the
//! UI and runner read, and its SSE change stream.

use std::sync::Arc;

use axum::http::StatusCode;
use axum::response::Response;
use jiff::Timestamp;
use serde::Serialize;
use tfstackplan_gate::events::{Graph, Phase};

use crate::logs::{PING_EVERY, send, sse, sse_response};
use crate::render::{count_stacks, is_apply_context, progress};
use crate::{App, http};

#[derive(Serialize)]
struct NullInt64 {
    #[serde(rename = "Int64")]
    int64: i64,
    #[serde(rename = "Valid")]
    valid: bool,
}

#[derive(Serialize)]
struct StoredGateTarget {
    #[serde(rename = "Class")]
    class: String,
    #[serde(rename = "Target")]
    target: String,
    #[serde(rename = "GrantName")]
    grant_name: String,
    #[serde(rename = "State")]
    state: String,
    #[serde(rename = "Requester")]
    requester: String,
}

/// Go's `executionResponse`: the embedded `store.Execution` fields first, then the
/// additions, with the outer `ProgressPct`/`ProgressLabel` shadowing the row's.
#[derive(Serialize)]
#[serde(rename_all = "PascalCase")]
struct ExecutionResponse {
    #[serde(rename = "ID")]
    id: String,
    repo: String,
    #[serde(rename = "SHA")]
    sha: String,
    #[serde(rename = "PR")]
    pr: i64,
    environment: String,
    #[serde(rename = "CheckRunID")]
    check_run_id: NullInt64,
    rev: i64,
    report_markdown: String,
    #[serde(rename = "LogURL")]
    log_url: String,
    status: String,
    status_context: String,
    phase: String,
    created_at: Timestamp,
    superseded_by: String,
    #[serde(rename = "verify_execution_id")]
    verify_execution_id: String,
    #[serde(rename = "graph")]
    graph: GoGraph,
    #[serde(rename = "gates")]
    gates: Vec<StoredGateTarget>,
    progress_pct: i64,
    progress_label: String,
    #[serde(rename = "change_reasons")]
    change_reasons: Vec<GoReason>,
}

/// `events.Graph` as Go encodes it: `[]` for both arrays (the store never returns
/// nil slices here).
#[derive(Serialize)]
struct GoGraph {
    stacks: Vec<tfstackplan_gate::events::StackState>,
    edges: Vec<tfstackplan_gate::events::Edge>,
}

/// `events.ChangeReason` with Go's nil `via` as null.
#[derive(Serialize)]
struct GoReason {
    stack: String,
    kind: String,
    via: Option<Vec<String>>,
}

impl From<serde_json::Value> for GoReason {
    fn from(v: serde_json::Value) -> GoReason {
        let s = |k: &str| {
            v.get(k)
                .and_then(|x| x.as_str())
                .unwrap_or_default()
                .to_string()
        };
        GoReason {
            stack: s("stack"),
            kind: s("kind"),
            via: v.get("via").and_then(|x| x.as_array()).map(|a| {
                a.iter()
                    .filter_map(|x| x.as_str().map(str::to_string))
                    .collect()
            }),
        }
    }
}

/// GET /api/execution/{id}.
pub(crate) async fn get(app: Arc<App>, id: String) -> Response {
    let id_o = id.clone();
    let loaded = app
        .db(move |s| {
            let Some(e) = s.get_execution(&id_o)? else {
                return Ok(None);
            };
            let g: tfstackplan_store::Result<Graph> = s.load_graph(&id_o);
            let gates = s.targets_for(e.pr, &e.environment).unwrap_or_default();
            let verify = s
                .latest_verify_execution_id(e.pr, &e.environment)
                .unwrap_or_default();
            Ok(Some((e, g, gates, verify)))
        })
        .await;
    let (e, g, gates, verify) = match loaded {
        Ok(Some(v)) => v,
        _ => return http::error(StatusCode::NOT_FOUND, "not found"),
    };
    let Ok(g) = g else {
        return http::error(StatusCode::INTERNAL_SERVER_ERROR, "load graph failed");
    };
    let (done, initialized, total) = count_stacks(&g.stacks);
    let weights = app
        .cfg
        .progress
        .as_ref()
        .map(|p| {
            p.for_kind(if is_apply_context(&e.status_context) {
                "apply"
            } else {
                "plan"
            })
        })
        .unwrap_or_default();
    let (_, fallback_label, fallback_pct) =
        progress(weights, &Phase::new(&e.phase), done, initialized, total);
    let change_reasons: Vec<GoReason> = if e.change_reasons.is_empty() {
        Vec::new()
    } else {
        serde_json::from_str::<Option<Vec<serde_json::Value>>>(&e.change_reasons)
            .ok()
            .flatten()
            .unwrap_or_default()
            .into_iter()
            .map(GoReason::from)
            .collect()
    };
    http::json_ok(&ExecutionResponse {
        id: e.id,
        repo: e.repo,
        sha: e.sha,
        pr: e.pr,
        environment: e.environment,
        check_run_id: NullInt64 {
            int64: e.check_run_id.unwrap_or(0),
            valid: e.check_run_id.is_some(),
        },
        rev: e.rev,
        report_markdown: e.report_markdown,
        log_url: e.log_url,
        status: e.status,
        status_context: e.status_context,
        phase: e.phase,
        created_at: e.created_at,
        superseded_by: e.superseded_by,
        verify_execution_id: verify,
        graph: GoGraph {
            stacks: g.stacks,
            edges: g.edges,
        },
        gates: gates
            .into_iter()
            .map(|t| StoredGateTarget {
                class: t.class,
                target: t.target,
                grant_name: t.grant_name,
                state: t.state,
                requester: t.requester,
            })
            .collect(),
        progress_pct: e.progress_pct.unwrap_or(fallback_pct),
        progress_label: e.progress_label.unwrap_or(fallback_label),
        change_reasons,
    })
}

/// GET /api/execution/{id}/events: "changed" and "superseded" notifications.
pub(crate) async fn events(app: Arc<App>, id: String) -> Response {
    let id_o = id.clone();
    if !matches!(app.db(move |s| s.get_execution(&id_o)).await, Ok(Some(_))) {
        return http::error(StatusCode::NOT_FOUND, "not found");
    }
    let mut sub = app.hub.subscribe(&format!("exec:{id}"));
    sse_response(move |tx| async move {
        let mut ping =
            tokio::time::interval_at(tokio::time::Instant::now() + PING_EVERY, PING_EVERY);
        loop {
            tokio::select! {
                () = tx.closed() => return,
                Some(msg) = sub.rx.recv() => {
                    let out = match msg.strip_prefix("superseded:") {
                        Some(new_id) => format!("event: superseded\ndata: {new_id}\n\n"),
                        None => sse("changed"),
                    };
                    if !send(&tx, out).await {
                        return;
                    }
                }
                _ = ping.tick() => {
                    if !send(&tx, ": ping\n\n".into()).await {
                        return;
                    }
                }
            }
        }
    })
}
