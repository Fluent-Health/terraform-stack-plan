//! The read endpoints for aggregating consumers (Go: `readhandlers.go`): recent
//! executions, approvals, a PR's view, the merge queue and the inspect family. The
//! response structs list their fields in the order Go's generated types declare
//! them, which is the order Go encodes them.

use std::collections::HashSet;
use std::sync::Arc;

use axum::http::StatusCode;
use axum::response::Response;
use jiff::Timestamp;
use serde::Serialize;
use serde_json::value::RawValue;
use tfstackplan_gate::events::Phase;
use tfstackplan_gate::reconcile::{self, ChangeSet, GateState, unmarshal_event};
use tfstackplan_store::{ExecutionSummary as Row, exec_stream_id};
use tracing::warn;

use crate::render::{is_apply_context, lock_blocked, lock_title, phase_label, progress};
use crate::{App, http};

const DEFAULT_EXECUTIONS_LIMIT: i64 = 100;
const MAX_EXECUTIONS_LIMIT: i64 = 1000;

/// One execution in a list (Go: `api.ExecutionSummary`).
#[derive(Serialize)]
pub(crate) struct ExecutionSummary {
    context: String,
    created_at: Timestamp,
    environment: String,
    id: String,
    log_url: String,
    phase: String,
    pr: i64,
    progress_label: String,
    progress_pct: i64,
    repo: String,
    sha: String,
    status: String,
    superseded_by: String,
}

impl App {
    fn summary(&self, e: Row) -> ExecutionSummary {
        let phase = Phase::new(&e.phase);
        let progress_label = e
            .progress_label
            .unwrap_or_else(|| phase_label(&phase, 0, 0, 0));
        let progress_pct = e.progress_pct.unwrap_or_else(|| {
            let weights = self
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
            progress(weights, &phase, 0, 0, 0).2
        });
        ExecutionSummary {
            context: e.status_context,
            created_at: e.created_at,
            environment: e.environment,
            id: e.id,
            log_url: e.log_url,
            phase: e.phase,
            pr: e.pr,
            progress_label,
            progress_pct,
            repo: e.repo,
            sha: e.sha,
            status: e.status,
            superseded_by: e.superseded_by,
        }
    }
}

/// GET /api/executions.
pub(crate) async fn list_executions(
    app: Arc<App>,
    pr: Option<i64>,
    limit: Option<i64>,
) -> Response {
    let limit = limit.unwrap_or(DEFAULT_EXECUTIONS_LIMIT);
    if !(1..=MAX_EXECUTIONS_LIMIT).contains(&limit) {
        return http::error(StatusCode::BAD_REQUEST, "limit must be between 1 and 1000");
    }
    let rows = app
        .db(move |s| match pr {
            Some(pr) => {
                let mut v = s.list_executions_for_pr(pr)?;
                v.truncate(usize::try_from(limit).unwrap_or(usize::MAX));
                Ok(v)
            }
            None => s.list_executions(limit),
        })
        .await;
    let Ok(rows) = rows else {
        return http::error(StatusCode::INTERNAL_SERVER_ERROR, "list executions");
    };
    let out: Vec<_> = rows.into_iter().map(|e| app.summary(e)).collect();
    http::json_ok(&out)
}

#[derive(Serialize)]
struct PendingApproval {
    class: String,
    environment: String,
    grant_name: String,
    pr: i64,
    repo: String,
    requester: String,
    state: String,
    target: String,
}

/// GET /api/approvals.
pub(crate) async fn list_approvals(app: Arc<App>) -> Response {
    let Ok(pending) = app.db(|s| s.pending_approvals()).await else {
        return http::error(StatusCode::INTERNAL_SERVER_ERROR, "list approvals");
    };
    let out: Vec<_> = pending
        .into_iter()
        .map(|p| PendingApproval {
            class: p.class,
            environment: p.environment,
            grant_name: p.grant_name,
            pr: p.pr,
            repo: p.repo,
            requester: p.requester,
            state: p.state,
            target: p.target,
        })
        .collect();
    http::json_ok(&out)
}

#[derive(Serialize)]
pub(crate) struct PrMeta {
    author_login: String,
    auto_merge: bool,
    body: String,
    head_ref: String,
    title: String,
    url: String,
}

impl From<tfstackplan_store::PrMeta> for PrMeta {
    fn from(m: tfstackplan_store::PrMeta) -> PrMeta {
        PrMeta {
            author_login: m.author_login,
            auto_merge: m.auto_merge,
            body: m.body,
            head_ref: m.head_ref,
            title: m.title,
            url: m.url,
        }
    }
}

/// A PR's merge readiness on this tier (Go: `PRMergeState`).
#[derive(Serialize)]
pub(crate) struct PrMergeState {
    blocker: String,
    check_conclusion: String,
    environment: String,
    merge_blocked: bool,
    required_check: String,
}

#[derive(Serialize)]
struct PrView {
    merge: PrMergeState,
    #[serde(skip_serializing_if = "Option::is_none")]
    meta: Option<PrMeta>,
    pr: i64,
    repo: String,
}

impl App {
    /// `pr`'s merge state on this tier: the required check's conclusion and the
    /// merge-lock verdict. Read-only.
    pub(crate) async fn pr_merge_state(&self, pr: i64) -> PrMergeState {
        let env = self.cfg.environment.clone();
        let mut state = PrMergeState {
            blocker: String::new(),
            check_conclusion: String::new(),
            environment: env.clone(),
            merge_blocked: false,
            required_check: self.merge_gate_check_name(&env),
        };
        let env_o = env.clone();
        let loaded = self
            .db(move |s| {
                let Some(id) = s.latest_reported_execution_id(pr, &env_o) else {
                    return Ok(None);
                };
                let stacks = s
                    .load_graph(&id)
                    .ok()
                    .map(|g| g.stacks.into_iter().map(|st| st.path).collect::<Vec<_>>());
                let snap = crate::checkrun::load_snapshot(s, &id)
                    .ok()
                    .flatten()
                    .map(|(snap, _)| snap);
                Ok(Some((stacks, snap)))
            })
            .await
            .ok()
            .flatten();
        let Some((stacks, snap)) = loaded else {
            let lock = crate::render::ApplyLockVerdict {
                state: "unverifiable".into(),
                reason: format!("no successful plan for #{pr}"),
                ..Default::default()
            };
            state.merge_blocked = lock_blocked(&lock);
            state.blocker = lock_title(&lock);
            return state;
        };
        let lock = self
            .eval_apply_lock(&env, pr, stacks.as_deref().unwrap_or_default(), self.now())
            .await;
        state.merge_blocked = lock_blocked(&lock);
        if state.merge_blocked {
            state.blocker = lock_title(&lock);
        }
        if let Some(snap) = snap {
            state.check_conclusion = crate::render::conclusion(&snap, &lock).into();
        }
        state
    }
}

/// GET /api/pr/{n}.
pub(crate) async fn get_pr(app: Arc<App>, pr: i64) -> Response {
    let loaded = app
        .db(move |s| Ok((s.list_executions_for_pr(pr)?, s.get_pr_meta_by_pr(pr)?)))
        .await;
    let Ok((execs, meta)) = loaded else {
        return http::error(StatusCode::INTERNAL_SERVER_ERROR, "get pr");
    };
    if meta.is_none() && execs.is_empty() {
        return http::error(StatusCode::NOT_FOUND, "unknown pr");
    }
    let repo = execs
        .first()
        .map(|e| e.repo.clone())
        .or_else(|| meta.as_ref().map(|m| m.repo.clone()))
        .unwrap_or_default();
    let merge = app.pr_merge_state(pr).await;
    http::json_ok(&PrView {
        merge,
        meta: meta.map(PrMeta::from),
        pr,
        repo,
    })
}

#[derive(Serialize)]
struct MergeQueueEntry {
    position: i64,
    pr: i64,
    state: String,
}

#[derive(Serialize)]
struct MergeQueue {
    branch: String,
    entries: Vec<MergeQueueEntry>,
}

/// GET /api/merge-queue: the live merge queue, empty on any failure.
pub(crate) async fn merge_queue(app: Arc<App>) -> Response {
    let mut out = MergeQueue {
        branch: String::new(),
        entries: Vec::new(),
    };
    if let Ok(rows) = app.db(|s| s.list_executions(1)).await
        && let Some(e) = rows.first()
    {
        match app.gh.merge_queue(&e.repo).await {
            Err(err) => warn!("serve: merge-queue read for {} failed: {err}", e.repo),
            Ok(res) => {
                out.branch = res.branch;
                out.entries = res
                    .entries
                    .into_iter()
                    .map(|e| MergeQueueEntry {
                        position: e.position,
                        pr: e.pr,
                        state: e.state,
                    })
                    .collect();
            }
        }
    }
    http::json_ok(&out)
}

/// One gate target row (Go: `api.InspectGrant`).
#[derive(Serialize, Clone)]
struct InspectGrant {
    #[serde(skip_serializing_if = "Option::is_none")]
    actual_state: Option<String>,
    class: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    drift_detected: Option<bool>,
    environment: String,
    grant_name: String,
    pr: i64,
    requester: String,
    state: String,
    target: String,
}

const GRANT_COLUMNS: &str = "pr, environment, class, target, COALESCE(grant_name,''), COALESCE(state,''), COALESCE(requester,'')";

fn grant_row(r: &rusqlite::Row<'_>) -> rusqlite::Result<InspectGrant> {
    Ok(InspectGrant {
        actual_state: None,
        class: r.get(2)?,
        drift_detected: None,
        environment: r.get(1)?,
        grant_name: r.get(4)?,
        pr: r.get(0)?,
        requester: r.get(6)?,
        state: r.get(5)?,
        target: r.get(3)?,
    })
}

/// GET /api/inspect/grants.
pub(crate) async fn inspect_grants(
    app: Arc<App>,
    state: Option<String>,
    live: Option<i64>,
) -> Response {
    let mut sql = format!("SELECT {GRANT_COLUMNS} FROM gate_targets");
    if state.as_deref() == Some("open") {
        sql.push_str(" WHERE state IN ('AWAITING', 'ACTIVATING', 'ACTIVE')");
    }
    let Ok(mut grants) = app
        .db(move |s| {
            s.read(|c| {
                let mut stmt = c.prepare(&sql)?;
                stmt.query_map([], grant_row)?.collect::<rusqlite::Result<Vec<_>>>()
            })
        })
        .await
    else {
        return http::error(StatusCode::INTERNAL_SERVER_ERROR, "query grants");
    };
    if live == Some(1)
        && let Some(approval) = &app.approval
    {
        let mut triggered = HashSet::new();
        for g in &mut grants {
            let Ok(live_grants) = approval.list_grants(&g.class, &g.target).await else {
                continue;
            };
            for lg in live_grants {
                if lg.request.pr == g.pr && lg.request.environment == g.environment {
                    let actual = lg.state.as_str();
                    if actual != g.state {
                        g.drift_detected = Some(true);
                        g.actual_state = Some(actual.into());
                        if triggered.insert((g.pr, g.environment.clone())) {
                            app.reconcile_background(g.pr, &g.environment);
                        }
                    }
                }
            }
        }
    }
    http::json_ok(&grants)
}

/// Go's `%T` of the gate state without the package prefix.
fn gate_name(g: &GateState) -> &'static str {
    match g {
        GateState::NotClassified => "NotClassified",
        GateState::Clean => "Clean",
        GateState::Pending(_) => "Pending",
        GateState::Satisfied(_) => "Satisfied",
        GateState::Blocked(_) => "Blocked",
    }
}

#[derive(Serialize)]
struct InspectGateReason {
    description: String,
    event_type: String,
    occurred_at: Timestamp,
}

#[derive(Serialize)]
struct InspectGateTarget {
    class: String,
    grant_name: String,
    requester: String,
    state: String,
    target: String,
}

#[derive(Serialize)]
struct InspectGateDetail {
    environment: String,
    gate_state: &'static str,
    pr: i64,
    reasons: Option<Vec<InspectGateReason>>,
    targets: Option<Vec<InspectGateTarget>>,
}

struct EventRow {
    version: i64,
    kind: String,
    occurred_at: String,
    data: Vec<u8>,
}

fn event_rows(
    s: &tfstackplan_store::Store,
    stream: &str,
    after: i64,
) -> tfstackplan_store::Result<Vec<EventRow>> {
    s.read(|c| {
        let mut stmt = c.prepare(
            "SELECT version, type, occurred_at, data FROM events WHERE stream_id = ?1 AND version > ?2 ORDER BY version",
        )?;
        stmt.query_map(rusqlite::params![stream, after], |r| {
                let data = match r.get_ref(3)? {
                    rusqlite::types::ValueRef::Text(t) | rusqlite::types::ValueRef::Blob(t) => t.to_vec(),
                    _ => Vec::new(),
                };
                Ok(EventRow {
                    version: r.get(0)?,
                    kind: r.get(1)?,
                    occurred_at: r.get(2)?,
                    data,
                })
            })?
            .collect::<rusqlite::Result<Vec<_>>>()
    })
}

/// A stored timestamp as Go's driver reads it (UTC when unzoned).
fn parse_time(raw: &str) -> Timestamp {
    if let Some(go) = raw.strip_suffix(" +0000 UTC") {
        return format!("{}Z", go.replacen(' ', "T", 1))
            .parse()
            .unwrap_or(Timestamp::UNIX_EPOCH);
    }
    raw.parse()
        .or_else(|_| format!("{}Z", raw.replacen(' ', "T", 1)).parse())
        .unwrap_or(Timestamp::UNIX_EPOCH)
}

/// GET /api/inspect/gate/{pr}/{env}.
pub(crate) async fn inspect_gate(app: Arc<App>, pr: i64, env: String) -> Response {
    let stream = exec_stream_id(pr, &env);
    let env_o = env.clone();
    let Ok((cs, rows)) = app
        .db(move |s| {
            let (cs, _) = s.load_gate(pr, &env_o)?;
            Ok((cs, event_rows(s, &stream, 0).unwrap_or_default()))
        })
        .await
    else {
        return http::error(StatusCode::INTERNAL_SERVER_ERROR, "load changeset");
    };
    let mut reasons: Option<Vec<InspectGateReason>> = None;
    let mut temp = ChangeSet::default();
    for r in rows {
        let Ok(ev) = unmarshal_event(&r.kind, &r.data) else {
            continue;
        };
        let prev = temp.gate.clone();
        temp = reconcile::evolve(temp, &ev);
        if prev != temp.gate {
            reasons
                .get_or_insert_with(Vec::new)
                .push(InspectGateReason {
                    description: format!("Event {} occurred", r.kind),
                    event_type: r.kind,
                    occurred_at: parse_time(&r.occurred_at),
                });
        }
    }
    let lease = cs.gate.lease();
    let targets = cs.gate.targets().map(|ts| {
        ts.iter()
            .map(|t| InspectGateTarget {
                class: t.class.clone(),
                grant_name: t.grant_name.clone(),
                requester: lease.requester.clone(),
                state: t.grant.as_str().into(),
                target: t.target.clone(),
            })
            .collect::<Vec<_>>()
    });
    // Go appends into a nil slice: no targets is null.
    let targets = targets.filter(|t| !t.is_empty());
    http::json_ok(&InspectGateDetail {
        environment: env,
        gate_state: gate_name(&cs.gate),
        pr,
        reasons,
        targets,
    })
}

#[derive(Serialize)]
struct InspectClaim {
    expires_at: Timestamp,
    pr: i64,
    stack: String,
}

#[derive(Serialize)]
struct InspectClaimsSet {
    claims: Option<Vec<InspectClaim>>,
    environment: String,
}

fn claim_list(
    cs: &tfstackplan_gate::claims::ClaimSet,
    only_pr: Option<i64>,
) -> Option<Vec<InspectClaim>> {
    let v: Vec<_> = cs
        .iter()
        .filter(|(_, c)| only_pr.is_none_or(|pr| c.pr == pr))
        .map(|(stack, c)| InspectClaim {
            expires_at: c.expires_at,
            pr: c.pr,
            stack: stack.clone(),
        })
        .collect();
    (!v.is_empty()).then_some(v)
}

/// GET /api/inspect/claims/{env}. Go ranges over a map, so its order is random;
/// this lists claims by stack.
pub(crate) async fn inspect_claims(app: Arc<App>, env: String) -> Response {
    let env_o = env.clone();
    let Ok(cs) = app.db(move |s| s.load_claims(&env_o)).await else {
        return http::error(StatusCode::INTERNAL_SERVER_ERROR, "load claims");
    };
    http::json_ok(&InspectClaimsSet {
        claims: claim_list(&cs, None),
        environment: env,
    })
}

#[derive(Serialize)]
struct InspectEvent<'a> {
    data: &'a RawValue,
    occurred_at: Timestamp,
    #[serde(rename = "type")]
    kind: String,
    version: i64,
}

/// GET /api/inspect/events/{stream}.
pub(crate) async fn inspect_events(app: Arc<App>, stream: String, after: Option<i64>) -> Response {
    let after = after.unwrap_or(0);
    let Ok(rows) = app.db(move |s| event_rows(s, &stream, after)).await else {
        return http::error(StatusCode::INTERNAL_SERVER_ERROR, "query events");
    };
    let raws: Vec<Box<RawValue>> = rows
        .iter()
        .map(|r| {
            // Go's encoder compacts a RawMessage (keeping key order) and HTML-escapes it.
            RawValue::from_string(crate::gojson::compact(&r.data))
                .unwrap_or_else(|_| RawValue::NULL.to_owned())
        })
        .collect();
    let out: Option<Vec<InspectEvent<'_>>> = (!rows.is_empty()).then(|| {
        rows.iter()
            .zip(&raws)
            .map(|(r, raw)| InspectEvent {
                data: raw,
                occurred_at: parse_time(&r.occurred_at),
                kind: r.kind.clone(),
                version: r.version,
            })
            .collect()
    });
    http::json_ok(&out)
}

#[derive(Serialize)]
struct InspectPrSummary {
    claims: Option<Vec<InspectClaim>>,
    executions: Vec<ExecutionSummary>,
    gate_state: &'static str,
    #[serde(skip_serializing_if = "Option::is_none")]
    meta: Option<PrMeta>,
    open_grants: Option<Vec<InspectGrant>>,
    pr: i64,
    repo: String,
}

const DISTINCT_PRS: &str = "SELECT DISTINCT pr FROM (
        SELECT pr FROM executions
        UNION
        SELECT pr FROM gate_targets
        UNION
        SELECT pr FROM pr_meta
    ) ORDER BY pr DESC";

fn distinct_prs(
    s: &tfstackplan_store::Store,
    limit: Option<i64>,
) -> tfstackplan_store::Result<Vec<i64>> {
    let sql = match limit {
        Some(n) => format!("{DISTINCT_PRS} LIMIT {n}"),
        None => DISTINCT_PRS.to_string(),
    };
    s.read(|c| {
        let mut stmt = c.prepare(&sql)?;
        let rows = stmt
            .query_map([], |r| r.get::<_, Option<i64>>(0))?
            .filter_map(Result::ok)
            .flatten()
            .collect();
        Ok(rows)
    })
}

/// GET /api/inspect/overview.
pub(crate) async fn inspect_overview(app: Arc<App>) -> Response {
    let Ok(prs) = app.db(|s| distinct_prs(s, Some(100))).await else {
        return http::error(StatusCode::INTERNAL_SERVER_ERROR, "query prs");
    };
    let env = app.cfg.environment.clone();
    let claims_state = if env.is_empty() {
        None
    } else {
        let e = env.clone();
        app.db(move |s| s.load_claims(&e)).await.ok()
    };
    let mut overview = Vec::new();
    for pr in prs {
        let env_o = env.clone();
        let loaded = app
            .db(move |s| {
                let execs = s.list_executions_for_pr(pr)?;
                let meta = s.get_pr_meta_by_pr(pr)?;
                let grants = s
                    .read(|c| {
                        let mut stmt = c.prepare(&format!(
                            "SELECT {GRANT_COLUMNS} FROM gate_targets WHERE pr = ?1"
                        ))?;
                        let rows = stmt
                            .query_map([pr], grant_row)?
                            .filter_map(Result::ok)
                            .collect::<Vec<_>>();
                        Ok(rows)
                    })
                    .unwrap_or_default();
                let gate = if env_o.is_empty() {
                    GateState::NotClassified
                } else {
                    s.load_gate(pr, &env_o)
                        .map(|(cs, _)| cs.gate)
                        .unwrap_or_default()
                };
                Ok((execs, meta, grants, gate))
            })
            .await;
        let Ok((execs, meta, grants, gate)) = loaded else {
            continue;
        };
        let repo = execs
            .first()
            .map(|e| e.repo.clone())
            .or_else(|| meta.as_ref().map(|m| m.repo.clone()))
            .unwrap_or_default();
        overview.push(InspectPrSummary {
            claims: claims_state
                .as_ref()
                .and_then(|cs| claim_list(cs, Some(pr))),
            executions: execs.into_iter().map(|e| app.summary(e)).collect(),
            gate_state: gate_name(&gate),
            meta: meta.map(PrMeta::from),
            open_grants: (!grants.is_empty()).then_some(grants),
            pr,
            repo,
        });
    }
    http::json_ok(&overview)
}

#[derive(Serialize)]
struct InspectPoolSlot {
    #[serde(skip_serializing_if = "Option::is_none")]
    elapsed_seconds: Option<i64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    environment: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    grant_name: Option<String>,
    occupied: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    pr: Option<i64>,
    requester: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    state: Option<String>,
}

#[derive(Serialize)]
struct InspectPoolWaitingPr {
    blocker_env: String,
    blocker_pr: i64,
    environment: String,
    pr: i64,
    reason: String,
}

#[derive(Serialize)]
struct InspectPoolSet {
    environment: String,
    slots: Vec<InspectPoolSlot>,
    waiting: Vec<InspectPoolWaitingPr>,
}

/// GET /api/inspect/pool.
pub(crate) async fn inspect_pool(app: Arc<App>) -> Response {
    let env = app.cfg.environment.clone();
    let mut slots: Vec<InspectPoolSlot> = app
        .cfg
        .requester_pool
        .iter()
        .map(|sa| InspectPoolSlot {
            elapsed_seconds: None,
            environment: None,
            grant_name: None,
            occupied: false,
            pr: None,
            requester: sa.clone(),
            state: None,
        })
        .collect();
    let env_o = env.clone();
    let (occupants, waiting) = app
        .db(move |s| {
            let occupants = s
                .read(|c| {
                    let mut stmt = c.prepare(
                        "SELECT pr, environment, COALESCE(grant_name,''), COALESCE(state,''), COALESCE(requester,''),
                                strftime('%s', 'now') - strftime('%s', updated_at)
                         FROM gate_targets WHERE state IN ('AWAITING', 'ACTIVATING', 'ACTIVE') AND environment = ?1",
                    )?;
                    let rows = stmt
                        .query_map([&env_o], |r| {
                            Ok((
                                r.get::<_, i64>(0)?,
                                r.get::<_, String>(1)?,
                                r.get::<_, String>(2)?,
                                r.get::<_, String>(3)?,
                                r.get::<_, String>(4)?,
                                r.get::<_, i64>(5)?,
                            ))
                        })?
                        .filter_map(Result::ok)
                        .collect::<Vec<_>>();
                    Ok(rows)
                })
                .unwrap_or_default();
            let mut waiting = Vec::new();
            for pr in distinct_prs(s, None).unwrap_or_default() {
                if let Ok((cs, _)) = s.load_gate(pr, &env_o)
                    && let GateState::Blocked(b) = cs.gate
                {
                    let reason = serde_json::to_value(b.by.reason)
                        .ok()
                        .and_then(|v| v.as_str().map(str::to_string))
                        .unwrap_or_default();
                    if reason == "slot_foreign_open" || reason == "slot_self" {
                        waiting.push(InspectPoolWaitingPr {
                            blocker_env: b.by.by_env,
                            blocker_pr: b.by.by_pr,
                            environment: env_o.clone(),
                            pr,
                            reason,
                        });
                    }
                }
            }
            Ok((occupants, waiting))
        })
        .await
        .unwrap_or_default();
    for (pr, env_s, name, state, req, elapsed) in occupants {
        if req.is_empty() {
            continue;
        }
        for slot in slots.iter_mut().filter(|s| s.requester == req) {
            slot.occupied = true;
            slot.pr = Some(pr);
            slot.environment = Some(env_s.clone());
            slot.grant_name = Some(name.clone());
            slot.state = Some(state.clone());
            slot.elapsed_seconds = Some(elapsed);
        }
    }
    http::json_ok(&InspectPoolSet {
        environment: env,
        slots,
        waiting,
    })
}
