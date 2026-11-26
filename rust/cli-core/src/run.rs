//! The pure parts of `run`: the control-plane event payloads, the terramate output
//! parsers (`list --why`, `run-graph` edges), the classification sidecar readers
//! that yield gates, categories and counts, and the apply gate's verdicts and
//! their operator guidance.

use std::collections::BTreeMap;
use std::sync::LazyLock;

use regex::Regex;
use serde::{Deserialize, Serialize};

pub mod classify;
pub mod status;

/// A stack in an execution's graph (`events.StackState`, as `run` sends it).
#[derive(Clone, Debug, Serialize, PartialEq, Eq)]
pub struct StackState {
    /// Stack path.
    pub path: String,
    /// Initial status (`pending`).
    #[serde(skip_serializing_if = "String::is_empty")]
    pub status: String,
}

/// A dependency edge: `from` runs before `to`.
#[derive(Clone, Debug, Serialize, Deserialize, PartialEq, Eq, PartialOrd, Ord)]
pub struct Edge {
    /// The stack that runs first.
    pub from: String,
    /// The stack that runs after it.
    pub to: String,
}

/// `events.Init`: registers an execution and its stack subgraph.
#[derive(Clone, Debug, Serialize, Default)]
pub struct Init {
    /// Execution id.
    pub id: String,
    /// `owner/name`.
    pub repo: String,
    /// Head commit.
    pub sha: String,
    /// Pull request number.
    pub pr: i64,
    /// Environment.
    pub environment: String,
    /// CI log link (unused by the CLI; always empty).
    pub log_url: String,
    /// The commit-status context this run drives (`apply/<env>`, `verify/<env>`).
    #[serde(skip_serializing_if = "String::is_empty")]
    pub context: String,
    /// The stacks, all pending.
    pub stacks: Vec<StackState>,
    /// Dependency edges among them.
    pub edges: Vec<Edge>,
}

/// `events.PhaseEvent`: a lifecycle phase transition.
#[derive(Clone, Debug, Serialize, Default)]
pub struct PhaseEvent {
    /// Execution id.
    pub id: String,
    /// `owner/name`.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub repo: String,
    /// Head commit.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub sha: String,
    /// Pull request number.
    #[serde(skip_serializing_if = "is_zero")]
    pub pr: i64,
    /// Environment.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub environment: String,
    /// The phase.
    pub phase: String,
    /// Progress label.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub label: String,
    /// Progress percentage.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub progress_pct: Option<i64>,
}

fn is_zero(n: &i64) -> bool {
    *n == 0
}

/// A matched classification category (`domain.Category`).
#[derive(Clone, Debug, Serialize, PartialEq, Eq)]
pub struct Category {
    /// Category name.
    pub name: String,
    /// Its icon.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub icon: String,
}

/// Per-kind operation counts (`domain.Counts`); zero fields are omitted.
#[derive(Clone, Debug, Serialize, Deserialize, Default, PartialEq, Eq)]
pub struct Counts {
    /// Creates.
    #[serde(default, skip_serializing_if = "is_zero")]
    pub add: i64,
    /// Updates.
    #[serde(default, skip_serializing_if = "is_zero")]
    pub change: i64,
    /// Destroys.
    #[serde(default, skip_serializing_if = "is_zero")]
    pub destroy: i64,
    /// Replacements.
    #[serde(default, skip_serializing_if = "is_zero")]
    pub replace: i64,
    /// Moves.
    #[serde(default, rename = "move", skip_serializing_if = "is_zero")]
    pub moved: i64,
    /// Imports.
    #[serde(default, skip_serializing_if = "is_zero")]
    pub import: i64,
    /// Forgets.
    #[serde(default, skip_serializing_if = "is_zero")]
    pub forget: i64,
    /// Sensitivity-only updates.
    #[serde(default, skip_serializing_if = "is_zero")]
    pub sensitivity_only: i64,
}

/// One approval needed: a class against a target.
#[derive(Clone, Debug, Serialize, PartialEq, Eq, PartialOrd, Ord)]
pub struct GateTarget {
    /// The gating class (e.g. `iam`).
    pub class: String,
    /// The target (e.g. a project).
    pub target: String,
}

/// Why terramate considers a stack changed.
#[derive(Clone, Debug, Serialize, PartialEq, Eq)]
pub struct ChangeReason {
    /// Stack path.
    pub stack: String,
    /// `direct`, `watch`, `module` or `unknown`.
    pub kind: String,
    /// What triggered it.
    pub via: Vec<String>,
}

/// `events.Finalize`: the terminal state of a run.
#[derive(Clone, Debug, Serialize, Default)]
pub struct Finalize {
    /// Execution id.
    pub id: String,
    /// The report (without the summary table).
    pub report_markdown: String,
    /// Each stack's plan section.
    #[serde(skip_serializing_if = "BTreeMap::is_empty")]
    pub stack_reports: BTreeMap<String, String>,
    /// Approvals needed.
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub gates: Vec<GateTarget>,
    /// Stacks adopting resources through a cross-state move.
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub moving: Vec<String>,
    /// The run failed.
    #[serde(skip_serializing_if = "std::ops::Not::not")]
    pub failed: bool,
    /// Each stack's categories.
    #[serde(skip_serializing_if = "BTreeMap::is_empty")]
    pub categories: BTreeMap<String, Vec<Category>>,
    /// Each stack's counts.
    #[serde(skip_serializing_if = "BTreeMap::is_empty")]
    pub counts: BTreeMap<String, Counts>,
    /// Why each stack changed.
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub change_reasons: Vec<ChangeReason>,
}

/// The `(pr, environment)` an apply gate is checked or revoked for.
#[derive(Clone, Debug, Serialize, Default)]
pub struct GateKey {
    /// Pull request number.
    pub pr: i64,
    /// Environment.
    pub environment: String,
}

/// Initial stack states: every stack pending.
pub fn pending(stacks: &[String]) -> Vec<StackState> {
    stacks
        .iter()
        .map(|s| StackState {
            path: s.clone(),
            status: "pending".to_string(),
        })
        .collect()
}

/// Go's `runner.NormalizeEdges`: maps run-graph endpoints (project-root-relative)
/// onto the listed stacks (an exact match wins, else the unique listed stack the
/// endpoint path-suffixes) and drops edges touching stacks outside the set.
pub fn normalize_edges(stacks: &[String], edges: &[Edge]) -> Vec<Edge> {
    let resolve = |endpoint: &str| -> Option<String> {
        if stacks.iter().any(|s| s == endpoint) {
            return Some(endpoint.to_string());
        }
        stacks
            .iter()
            .find(|s| endpoint.ends_with(&format!("/{s}")))
            .cloned()
    };
    edges
        .iter()
        .filter_map(|e| {
            Some(Edge {
                from: resolve(&e.from)?,
                to: resolve(&e.to)?,
            })
        })
        .collect()
}

static WHY: LazyLock<Regex> = LazyLock::new(|| {
    #[expect(
        clippy::expect_used,
        reason = "a constant pattern; covered by the tests"
    )]
    crate::goregex::compile(r#"^([^\s]+)\s+-\s+stack changed because "(.*)" changed because (.*)$"#)
        .expect("valid why pattern")
});

/// Go's `runner.ParseLine`: one `terramate list --changed --why` line.
pub fn parse_why_line(line: &str) -> ChangeReason {
    if let Some(m) = WHY.captures(line) {
        let (stack, trigger, reason) = (&m[1], &m[2], &m[3]);
        let kind = if reason.contains("module") {
            "module"
        } else if reason.contains("watch") || trigger.contains("components") {
            "watch"
        } else {
            "direct"
        };
        return ChangeReason {
            stack: stack.to_string(),
            kind: kind.to_string(),
            via: vec![trigger.to_string()],
        };
    }
    if let Some((stack, reason)) = line.split_once(" - ") {
        let (stack, reason) = (stack.trim(), reason.trim());
        let kind = if reason == "stack has unmerged changes" {
            "direct"
        } else if reason.contains("module") {
            "module"
        } else if reason.contains("watch") {
            "watch"
        } else {
            "direct"
        };
        return ChangeReason {
            stack: stack.to_string(),
            kind: kind.to_string(),
            via: vec![stack.to_string()],
        };
    }
    ChangeReason {
        stack: line.to_string(),
        kind: "unknown".to_string(),
        via: Vec::new(),
    }
}

#[derive(Deserialize, Default)]
struct SidecarCategory {
    #[serde(default)]
    category: String,
    #[serde(default)]
    icon: Option<String>,
    #[serde(default)]
    attributes: BTreeMap<String, Vec<String>>,
}

#[derive(Deserialize, Default)]
struct SidecarStack {
    #[serde(default)]
    categories: Vec<SidecarCategory>,
    #[serde(default)]
    counts: Counts,
}

#[derive(Deserialize, Default)]
struct SidecarSummary {
    #[serde(default)]
    categories: Vec<SidecarCategory>,
}

#[derive(Deserialize, Default)]
struct Sidecar {
    #[serde(default)]
    stacks: BTreeMap<String, SidecarStack>,
    #[serde(default)]
    summary: SidecarSummary,
}

/// The non-gating category a stack carries when it adopts resources through a move.
pub const MOVE_CATEGORY: &str = "move";

/// What the classify pass reads from the classification sidecar.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct SidecarView {
    /// Approvals needed: each gating class's distinct emitted-attribute values, sorted.
    pub gates: Vec<GateTarget>,
    /// Stacks carrying the `move` category, sorted.
    pub moving: Vec<String>,
    /// Each stack's categories (stacks with none are left out).
    pub categories: BTreeMap<String, Vec<Category>>,
    /// Each stack's counts.
    pub counts: BTreeMap<String, Counts>,
}

/// Go's `gatesFromSidecar`, `categoriesFromSidecar` and `countsFromSidecar` in one
/// read. `gating` names the classes that have a `class` binding.
pub fn read_sidecar(data: &[u8], gating: &[String]) -> Result<SidecarView, String> {
    let doc: Sidecar = serde_json::from_slice(data).map_err(|e| e.to_string())?;
    let mut view = SidecarView::default();
    for c in &doc.summary.categories {
        if !gating.contains(&c.category) {
            continue;
        }
        let mut targets: Vec<&String> = c
            .attributes
            .values()
            .flatten()
            .filter(|v| !v.is_empty())
            .collect();
        targets.sort();
        targets.dedup();
        for t in targets {
            view.gates.push(GateTarget {
                class: c.category.clone(),
                target: t.clone(),
            });
        }
    }
    for (path, entry) in &doc.stacks {
        if entry.categories.iter().any(|c| c.category == MOVE_CATEGORY) {
            view.moving.push(path.clone());
        }
        let cats: Vec<Category> = entry
            .categories
            .iter()
            .map(|c| Category {
                name: c.category.clone(),
                icon: c.icon.clone().unwrap_or_default(),
            })
            .collect();
        if !cats.is_empty() {
            view.categories.insert(path.clone(), cats);
        }
        view.counts.insert(path.clone(), entry.counts.clone());
    }
    Ok(view)
}

/// The classify gate set for the build log: `none`, or `class/target, …`.
pub fn gate_summary(gates: &[GateTarget]) -> String {
    if gates.is_empty() {
        return "none".to_string();
    }
    gates
        .iter()
        .map(|g| format!("{}/{}", g.class, g.target))
        .collect::<Vec<_>>()
        .join(", ")
}

/// The typed outcome of the apply gate pre-check (`runner.VerdictKind`).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum VerdictKind {
    /// Apply may proceed (including "no server").
    Satisfied,
    /// The PR never finalized a plan (`GATE-001`).
    NotClassified,
    /// A required grant is not active, or an unknown code (`GATE-002`).
    NotSatisfied,
    /// Gate state could not be confirmed (`GATE-003`).
    Unconfirmable,
    /// The server is unreachable.
    Unreachable,
}

/// The gate pre-check's verdict.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Verdict {
    /// Its kind.
    pub kind: VerdictKind,
    /// The leased service account to apply as (only when satisfied).
    pub requester: String,
    /// The underlying error, for the log.
    pub err: String,
}

/// Classifies a `/api/gate/check` response (Go's `Client.GateCheck` after the request).
pub fn gate_verdict(status: u16, body: &[u8]) -> Verdict {
    let json: serde_json::Value = serde_json::from_slice(body).unwrap_or_default();
    let field = |k: &str| {
        json.get(k)
            .and_then(|v| v.as_str())
            .unwrap_or_default()
            .to_string()
    };
    if status / 100 == 2 {
        return Verdict {
            kind: VerdictKind::Satisfied,
            requester: field("requester"),
            err: String::new(),
        };
    }
    let code = field("code");
    let mut err = format!("gate check: {status}: {}", field("message"));
    if code.is_empty() && (status == 401 || status == 403) {
        err = format!(
            "gate check: {status}: API auth rejected — check TFSTACKPLAN_AUDIENCE and the server's api_auth principals/scopes"
        );
    }
    let kind = match code.as_str() {
        "GATE-001" => VerdictKind::NotClassified,
        "GATE-003" => VerdictKind::Unconfirmable,
        _ => VerdictKind::NotSatisfied,
    };
    Verdict {
        kind,
        requester: String::new(),
        err,
    }
}

/// Go's `printGateVerdict`: what to tell the operator when the gate refuses.
pub fn gate_guidance(v: &Verdict, server: &str, pr: i64) -> String {
    let mut out = format!("tfstackplan run apply: refusing to apply — {}\n", v.err);
    match v.kind {
        VerdictKind::NotClassified | VerdictKind::NotSatisfied => {
            out.push_str(
                "  state: AWAITING_APPROVAL — the PR's approval grant(s) are not yet active.\n",
            );
            if server.is_empty() {
                out.push_str(&format!(
                    "  next: approve the grant(s) for PR #{pr}, then re-run this tier's apply.\n"
                ));
            } else {
                out.push_str(&format!(
                    "  next: approve the grant(s) at {server} (PR #{pr}), then re-run this tier's apply.\n"
                ));
            }
        }
        VerdictKind::Unconfirmable | VerdictKind::Unreachable => {
            out.push_str(
                "  state: GATE_UNREACHABLE — the approval server is degraded/unreachable.\n",
            );
            out.push_str("  next: see the break-glass local-apply runbook in docs/ci-cd.md.\n");
        }
        VerdictKind::Satisfied => {
            out.push_str("  state: gate not satisfied.\n");
            if !server.is_empty() {
                out.push_str(&format!(
                    "  next: review the gate at {server} (PR #{pr}), then re-run this tier's apply.\n"
                ));
            }
        }
    }
    out
}

/// Go's `strconv.Atoi(s)` or 0 (`atoiOr0`).
pub fn atoi_or_0(s: &str) -> i64 {
    s.parse().unwrap_or(0)
}

#[cfg(test)]
mod tests;
