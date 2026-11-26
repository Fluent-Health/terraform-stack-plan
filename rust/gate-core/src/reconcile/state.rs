use std::collections::BTreeMap;

use serde::{Deserialize, Serialize};

use crate::approval::GrantState;
use crate::json::nullable;

/// The reconciliation unit: one PR's work in one environment.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct ChangeSet {
    pub pr: i64,
    pub environment: String,
    pub gate: GateState,
    pub check_override: Option<CheckOverride>,
    /// Serve-initiated CI runs, keyed by kind (`plan`/`apply`). Only the run-start
    /// lifecycle lives here; once the runner reports, the execution aggregate
    /// carries the execution facts.
    pub runs: BTreeMap<String, Run>,
}

/// An admin's override of the check conclusion.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct CheckOverride {
    pub check_name: String,
    pub conclusion: String,
    pub actor: String,
    pub reason: String,
}

/// The plan run kind.
pub const RUN_KIND_PLAN: &str = "plan";
/// The apply run kind.
pub const RUN_KIND_APPLY: &str = "apply";

/// The serve-initiated lifecycle of one CI run.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Run {
    #[serde(rename = "ExecutionID")]
    pub execution_id: String,
    /// [`RUN_KIND_PLAN`] or [`RUN_KIND_APPLY`].
    #[serde(rename = "Kind")]
    pub kind: String,
    #[serde(rename = "SHA")]
    pub sha: String,
    #[serde(rename = "Branch")]
    pub branch: String,
    /// Bumps on rerun or retry; part of the deterministic execution id.
    #[serde(rename = "Attempt")]
    pub attempt: i64,
    /// The executor backend's reference, empty until started.
    #[serde(rename = "BuildRef")]
    pub build_ref: String,
    #[serde(rename = "Phase")]
    pub phase: RunPhase,
}

impl Run {
    /// Whether the run is still in flight.
    pub fn live(&self) -> bool {
        matches!(self.phase, RunPhase::Queued | RunPhase::Started)
    }
}

/// The run-start lifecycle phase.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum RunPhase {
    /// The execution exists; `StartRun` is pending or issued.
    #[default]
    Queued,
    /// The executor accepted the build.
    Started,
    /// The executor refused, or the start errored.
    StartFailed,
    /// A newer SHA replaced this run.
    Superseded,
    /// The runner took over and finalized.
    Completed,
}

/// The pooled requester identity leased once per change set. Empty means
/// undecided.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Lease {
    #[serde(rename = "Requester")]
    pub requester: String,
}

/// One (class, target) gate and its observed grant.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Target {
    #[serde(rename = "Class")]
    pub class: String,
    #[serde(rename = "Target")]
    pub target: String,
    /// The backend-assigned id, empty until created.
    #[serde(rename = "GrantName")]
    pub grant_name: String,
    #[serde(rename = "Grant")]
    pub grant: GrantState,
}

/// A gate is exactly one of these.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub enum GateState {
    /// Never planned: apply fails closed.
    #[default]
    NotClassified,
    /// Classified with zero gates: apply passes.
    Clean,
    Pending(Pending),
    Satisfied(Satisfied),
    Blocked(Blocked),
}

/// Some target still waits for an active grant.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Pending {
    #[serde(rename = "Targets", deserialize_with = "nullable")]
    pub targets: Vec<Target>,
    #[serde(rename = "Lease")]
    pub lease: Lease,
}

/// Every grant is active.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Satisfied {
    #[serde(rename = "Targets", deserialize_with = "nullable")]
    pub targets: Vec<Target>,
    #[serde(rename = "Lease")]
    pub lease: Lease,
}

/// Denied, expired, revoked, or a slot collision.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Blocked {
    #[serde(rename = "Targets", default, deserialize_with = "nullable")]
    pub targets: Vec<Target>,
    #[serde(rename = "Lease", default)]
    pub lease: Lease,
    #[serde(rename = "By")]
    pub by: Blocker,
}

/// Why a gate is blocked.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Blocker {
    #[serde(rename = "Reason")]
    pub reason: BlockReason,
    /// The slot blocker's PR (0 if not applicable).
    #[serde(rename = "ByPR", default)]
    pub by_pr: i64,
    /// The slot blocker's environment.
    #[serde(rename = "ByEnv", default)]
    pub by_env: String,
}

impl Blocker {
    /// A blocker with only a reason.
    pub fn new(reason: BlockReason) -> Blocker {
        Blocker {
            reason,
            by_pr: 0,
            by_env: String::new(),
        }
    }
}

/// The reason a gate is blocked.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
pub enum BlockReason {
    #[serde(rename = "denied")]
    Denied,
    #[serde(rename = "expired")]
    Expired,
    #[serde(rename = "revoked")]
    Revoked,
    /// Another open PR holds the slot.
    #[serde(rename = "slot_foreign_open")]
    SlotForeign,
    /// The same PR, in another environment, holds the slot.
    #[serde(rename = "slot_self")]
    SlotSelf,
    #[serde(rename = "backend_error")]
    BackendError,
}

impl GateState {
    /// The targets of a stateful gate (`Pending`, `Satisfied`, `Blocked`); `None`
    /// for `NotClassified` and `Clean` (Go's nil slice).
    pub fn targets(&self) -> Option<&[Target]> {
        match self {
            GateState::Pending(v) => Some(&v.targets),
            GateState::Satisfied(v) => Some(&v.targets),
            GateState::Blocked(v) => Some(&v.targets),
            GateState::NotClassified | GateState::Clean => None,
        }
    }

    /// The lease of a stateful gate; empty otherwise.
    pub fn lease(&self) -> Lease {
        match self {
            GateState::Pending(v) => v.lease.clone(),
            GateState::Satisfied(v) => v.lease.clone(),
            GateState::Blocked(v) => v.lease.clone(),
            GateState::NotClassified | GateState::Clean => Lease::default(),
        }
    }

    /// Replaces the targets, keeping the variant and its lease (Go: `withTargets`).
    pub(crate) fn with_targets(self, targets: Vec<Target>) -> GateState {
        match self {
            GateState::Pending(v) => GateState::Pending(Pending { targets, ..v }),
            GateState::Satisfied(v) => GateState::Satisfied(Satisfied { targets, ..v }),
            GateState::Blocked(v) => GateState::Blocked(Blocked { targets, ..v }),
            other => other,
        }
    }

    /// Replaces targets and lease, keeping the variant (Go: `withTargetsAndLease`).
    pub(crate) fn with_targets_and_lease(self, targets: Vec<Target>, lease: Lease) -> GateState {
        match self {
            GateState::Pending(_) => GateState::Pending(Pending { targets, lease }),
            GateState::Satisfied(_) => GateState::Satisfied(Satisfied { targets, lease }),
            GateState::Blocked(v) => GateState::Blocked(Blocked {
                targets,
                lease,
                by: v.by,
            }),
            other => other,
        }
    }
}

#[cfg(test)]
mod tests;
