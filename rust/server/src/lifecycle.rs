//! The PR lifecycle bar (Go: `lifecyclehandlers.go`): observed runner phases mapped
//! onto display segments, plus the relevant template segments still pending.

use std::collections::{BTreeMap, HashMap};
use std::sync::Arc;

use axum::http::StatusCode;
use axum::response::Response;
use jiff::Timestamp;
use serde::Serialize;
use tfstackplan_gate::events::{Counts, Graph, Status};
use tfstackplan_store::{ExecutionSummary, GateTargetRow, PhaseRow};

use crate::{App, http};

const TEMPLATE: [(&str, &str, &str); 9] = [
    ("prepare", "prepare", "plan"),
    ("init", "init", "plan"),
    ("plan", "plan", "plan"),
    ("classify", "classify", "plan"),
    ("report", "report", "plan"),
    ("approve", "approve", "gate"),
    ("moves", "state moves", "apply"),
    ("apply", "apply", "apply"),
    ("verify", "verify", "verify"),
];

fn canonical_rank(key: &str) -> Option<i64> {
    TEMPLATE
        .iter()
        .position(|(k, _, _)| *k == key)
        .and_then(|i| i64::try_from(i * 10).ok())
}

/// One lifecycle segment (Go: `api.LifecyclePhase`).
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub(crate) struct LifecyclePhase {
    #[serde(skip_serializing_if = "Option::is_none")]
    pub context: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub detail: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub ended_at: Option<Timestamp>,
    pub key: String,
    pub label: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub progress_pct: Option<i64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub result: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub started_at: Option<Timestamp>,
    pub state: String,
}

/// The display segment of a raw phase in its execution's context.
pub(crate) fn segment_for_phase(
    ctx: &str,
    phase: &str,
) -> Option<(&'static str, &'static str, &'static str)> {
    match ctx {
        "apply" => match phase {
            "warming" | "image" => Some(("apply", "apply", "warming caches")),
            "initializing" => Some(("apply", "apply", "initializing stacks")),
            "moves" => Some(("moves", "state moves", "")),
            "applying" => Some(("apply", "apply", "")),
            "classify" => Some(("apply", "apply", "classifying results")),
            "report" => Some(("apply", "apply", "writing report")),
            "testing" | "verifying" => Some(("verify", "verify", "")),
            _ => None,
        },
        "verify" => match phase {
            "warming" | "image" => Some(("verify", "verify", "preparing")),
            "testing" | "verifying" => Some(("verify", "verify", "")),
            _ => None,
        },
        _ => match phase {
            "warming" | "image" => Some(("prepare", "prepare", "")),
            "initializing" => Some(("init", "init", "")),
            "moves" => Some(("moves", "state moves", "")),
            "planning" => Some(("plan", "plan", "")),
            "classify" => Some(("classify", "classify", "")),
            "report" => Some(("report", "report", "")),
            "applying" => Some(("apply", "apply", "")),
            "testing" | "verifying" => Some(("verify", "verify", "")),
            _ => None,
        },
    }
}

fn rollup_counts(g: &Graph) -> Counts {
    let mut c = Counts::default();
    for sc in g.stacks.iter().filter_map(|s| s.counts.as_ref()) {
        c.add += sc.add;
        c.change += sc.change;
        c.destroy += sc.destroy;
        c.replace += sc.replace;
        c.moved += sc.moved;
    }
    c
}

/// "+a ~b ±c −d ↔e", zero kinds omitted.
pub(crate) fn counts_summary(c: &Counts) -> String {
    let mut p = Vec::new();
    if c.add != 0 {
        p.push(format!("+{}", c.add));
    }
    if c.change != 0 {
        p.push(format!("~{}", c.change));
    }
    if c.replace != 0 {
        p.push(format!("±{}", c.replace));
    }
    if c.destroy != 0 {
        p.push(format!("−{}", c.destroy));
    }
    if c.moved != 0 {
        p.push(format!("↔{}", c.moved));
    }
    p.join(" ")
}

/// The approve/apply view of a gate.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub(crate) struct GateSummary {
    pub count: usize,
    pub approve_state: &'static str,
    pub apply_wait: &'static str,
}

pub(crate) fn derive_gate_summary(targets: &[GateTargetRow]) -> GateSummary {
    let mut gs = GateSummary {
        count: targets.len(),
        ..GateSummary::default()
    };
    if targets.is_empty() {
        return gs;
    }
    let (mut all_active, mut blocked, mut activating) = (true, false, false);
    for t in targets {
        match t.state.as_str() {
            "ACTIVE" => {}
            "DENIED" | "REVOKED" => {
                blocked = true;
                all_active = false;
            }
            "ACTIVATING" => {
                activating = true;
                all_active = false;
            }
            _ => all_active = false,
        }
    }
    if blocked {
        gs.approve_state = "failed";
        gs.apply_wait = "blocked on a denied/revoked approval";
    } else if all_active {
        gs.approve_state = "done";
    } else if activating {
        gs.approve_state = "now";
        gs.apply_wait = "waits on approval";
    } else {
        gs.approve_state = "pending";
        gs.apply_wait = "waits on approval";
    }
    gs
}

pub(crate) fn exec_context_kind(status_context: &str) -> &'static str {
    match status_context.split('/').next().unwrap_or_default() {
        "plan" => "plan",
        "apply" => "apply",
        "verify" => "verify",
        _ => "other",
    }
}

fn newer(a: &ExecutionSummary, b: &ExecutionSummary) -> bool {
    a.created_at > b.created_at || (a.created_at == b.created_at && a.id > b.id)
}

/// Keeps the executions of the PR's current head SHA (the newest execution's).
pub(crate) fn scope_to_head_sha(execs: Vec<ExecutionSummary>) -> Vec<ExecutionSummary> {
    let Some(head) = execs
        .iter()
        .fold(None::<&ExecutionSummary>, |best, e| match best {
            Some(b) if !newer(e, b) => Some(b),
            _ => Some(e),
        })
    else {
        return execs;
    };
    let sha = head.sha.clone();
    execs.into_iter().filter(|e| e.sha == sha).collect()
}

/// The newest execution per status context, oldest first.
pub(crate) fn newest_per_status_context(execs: Vec<ExecutionSummary>) -> Vec<ExecutionSummary> {
    let mut best: HashMap<String, ExecutionSummary> = HashMap::new();
    for e in execs {
        match best.get(&e.status_context) {
            Some(cur) if !newer(&e, cur) => {}
            _ => {
                best.insert(e.status_context.clone(), e);
            }
        }
    }
    let mut out: Vec<_> = best.into_values().collect();
    out.sort_by(|a, b| {
        a.created_at
            .cmp(&b.created_at)
            .then_with(|| a.id.cmp(&b.id))
    });
    out
}

fn has_moving_stacks(g: &Graph) -> bool {
    g.stacks.iter().any(|s| s.status == Status::Moving)
}

fn segment_progress(key: &str, plan: &Graph, apply: &Graph) -> Option<i64> {
    let (g, terminal): (&Graph, fn(Status) -> bool) = match key {
        "plan" => (plan, |s| {
            !matches!(
                s,
                Status::Pending | Status::Running | Status::Initializing | Status::Initialized
            )
        }),
        "apply" => (apply, |s| {
            matches!(
                s,
                Status::Safe | Status::Nochange | Status::Failed | Status::Aborted
            )
        }),
        _ => return None,
    };
    let total = i64::try_from(g.stacks.len()).ok()?;
    if total == 0 {
        return None;
    }
    let done = i64::try_from(g.stacks.iter().filter(|s| terminal(s.status)).count()).ok()?;
    Some(done * 100 / total)
}

fn derive_result(
    key: &str,
    ctx: &str,
    plan: &Counts,
    apply: &Counts,
    gate: &GateSummary,
    state: &str,
) -> String {
    match key {
        "plan" | "report" => counts_summary(if ctx == "apply" { apply } else { plan }),
        "classify" => match gate.count {
            0 => "no gates".into(),
            1 => "1 gate required".into(),
            n => format!("{n} gates required"),
        },
        "approve" if gate.count > 0 && state != "done" => gate.apply_wait.into(),
        "apply" if state == "pending" && !gate.apply_wait.is_empty() => gate.apply_wait.into(),
        _ => String::new(),
    }
}

fn some_nonempty(s: String) -> Option<String> {
    (!s.is_empty()).then_some(s)
}

/// The pure fold.
pub(crate) fn lifecycle_fold(
    execs: &[ExecutionSummary],
    phases_by_exec: &HashMap<String, Vec<PhaseRow>>,
    plan_graph: &Graph,
    apply_graph: &Graph,
    gate: &GateSummary,
) -> Vec<LifecyclePhase> {
    let (plan_counts, apply_counts) = (rollup_counts(plan_graph), rollup_counts(apply_graph));
    let mut failed = HashMap::new();
    let mut succeeded = HashMap::new();
    let mut ctx_by_exec = HashMap::new();
    let mut has_ctx: HashMap<&str, bool> = HashMap::new();
    let mut apply_succeeded = false;
    for e in execs {
        failed.insert(e.id.as_str(), e.status == "failure");
        succeeded.insert(e.id.as_str(), e.status == "success");
        let k = exec_context_kind(&e.status_context);
        ctx_by_exec.insert(e.id.as_str(), k);
        has_ctx.insert(k, true);
        if k == "apply" && e.status == "success" {
            apply_succeeded = true;
        }
    }
    let mut all: Vec<(&str, &PhaseRow)> = Vec::new();
    for e in execs {
        for r in phases_by_exec.get(&e.id).into_iter().flatten() {
            all.push((e.id.as_str(), r));
        }
    }
    all.sort_by_key(|a| a.1.at);
    let mut last_row_of = HashMap::new();
    for (i, (id, _)) in all.iter().enumerate() {
        last_row_of.insert(*id, i);
    }
    let mut out: Vec<LifecyclePhase> = Vec::new();
    let mut ranks: Vec<i64> = Vec::new();
    let mut seg_at: BTreeMap<String, usize> = BTreeMap::new();
    let mut last_canonical_rank = -10;
    for (i, (id, row)) in all.iter().enumerate() {
        let ctx = ctx_by_exec.get(id).copied().unwrap_or("other");
        let (key, label, detail) = segment_for_phase(ctx, &row.phase)
            .map_or((row.phase.clone(), row.phase.clone(), ""), |(k, l, d)| {
                (k.to_string(), l.to_string(), d)
            });
        let mut state = "done";
        if last_row_of.get(id) == Some(&i) {
            state = if failed.get(id).copied().unwrap_or(false) {
                "failed"
            } else if succeeded.get(id).copied().unwrap_or(false) {
                "done"
            } else {
                "now"
            };
        }
        let ended_at = all.get(i + 1).map(|(_, r)| r.at);
        let detail = (state == "now" && !detail.is_empty()).then(|| detail.to_string());
        let result = some_nonempty(derive_result(
            &key,
            ctx,
            &plan_counts,
            &apply_counts,
            gate,
            state,
        ));
        if let Some(&j) = seg_at.get(&key) {
            out[j].state = state.into();
            out[j].ended_at = ended_at;
            out[j].detail = detail;
            if result.is_some() {
                out[j].result = result;
            }
            continue;
        }
        let rank = match canonical_rank(&key) {
            Some(r) => {
                last_canonical_rank = r;
                r
            }
            None => last_canonical_rank + 5,
        };
        seg_at.insert(key.clone(), out.len());
        out.push(LifecyclePhase {
            context: Some(ctx.into()),
            detail,
            ended_at,
            key,
            label,
            progress_pct: None,
            result,
            started_at: Some(row.at),
            state: state.into(),
        });
        ranks.push(rank);
    }
    let moves_known = has_moving_stacks(plan_graph) || has_moving_stacks(apply_graph);
    let has = |k: &str| has_ctx.get(k).copied().unwrap_or(false);
    for (key, label, context) in TEMPLATE {
        if seg_at.contains_key(key) {
            continue;
        }
        let relevant = match key {
            "init" => false,
            "prepare" | "plan" | "classify" | "report" => has("plan"),
            "approve" => !gate.approve_state.is_empty(),
            "moves" => has("apply") && moves_known,
            "apply" => has("apply"),
            "verify" => has("apply") || has("verify"),
            _ => true,
        };
        if !relevant {
            continue;
        }
        let mut state = if key == "approve" {
            gate.approve_state
        } else {
            "pending"
        };
        if key == "verify" && apply_succeeded {
            state = "done";
        }
        out.push(LifecyclePhase {
            context: Some(context.into()),
            detail: None,
            ended_at: None,
            key: key.into(),
            label: label.into(),
            progress_pct: None,
            result: some_nonempty(derive_result(
                key,
                context,
                &plan_counts,
                &apply_counts,
                gate,
                state,
            )),
            started_at: None,
            state: state.into(),
        });
        ranks.push(canonical_rank(key).unwrap_or_default());
    }
    let mut idx: Vec<usize> = (0..out.len()).collect();
    idx.sort_by_key(|&i| ranks[i]);
    let mut sorted: Vec<LifecyclePhase> = idx.into_iter().map(|i| out[i].clone()).collect();
    for seg in sorted.iter_mut().filter(|s| s.state == "now") {
        seg.progress_pct = segment_progress(&seg.key, plan_graph, apply_graph);
    }
    sorted
}

/// GET /api/lifecycle?pr=.
pub(crate) async fn get(app: Arc<App>, pr: i64) -> Response {
    let env = app.cfg.environment.clone();
    let Ok(execs) = app.db(move |s| s.list_executions_for_pr(pr)).await else {
        return http::error(StatusCode::INTERNAL_SERVER_ERROR, "list executions");
    };
    let live: Vec<_> = execs
        .into_iter()
        .filter(|e| e.superseded_by.is_empty() && e.environment == env)
        .collect();
    let live = newest_per_status_context(scope_to_head_sha(live));
    let ids: Vec<(String, &'static str)> = live
        .iter()
        .map(|e| (e.id.clone(), exec_context_kind(&e.status_context)))
        .collect();
    let env_o = env.clone();
    let loaded = app
        .db(move |s| {
            let mut phases = HashMap::new();
            let (mut plan, mut apply) = (Graph::default(), Graph::default());
            for (id, kind) in &ids {
                phases.insert(id.clone(), s.phases_for(id)?);
                match *kind {
                    "plan" => {
                        if let Ok(g) = s.load_graph(id) {
                            plan = g;
                        }
                    }
                    "apply" => {
                        if let Ok(g) = s.load_graph(id) {
                            apply = g;
                        }
                    }
                    _ => {}
                }
            }
            let targets = s.targets_for(pr, &env_o).unwrap_or_default();
            Ok((phases, plan, apply, targets))
        })
        .await;
    let Ok((phases, plan, apply, targets)) = loaded else {
        return http::error(StatusCode::INTERNAL_SERVER_ERROR, "load phases");
    };
    http::json_ok(&lifecycle_fold(
        &live,
        &phases,
        &plan,
        &apply,
        &derive_gate_summary(&targets),
    ))
}

#[cfg(test)]
mod tests;
