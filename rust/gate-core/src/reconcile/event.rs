use serde::{Deserialize, Serialize};

use crate::approval::GrantState;
use crate::events::GateTarget;
use crate::json::nullable;

use super::{BlockReason, wrap_variants};

/// A past-tense domain fact produced by [`decide`](super::decide) and folded by
/// [`evolve`](super::evolve). Presentation (check run, SSE) is never an event;
/// [`react`](super::react) derives it.
///
/// Each variant's JSON is the Go struct's: untagged Go fields keep their Go names
/// (`Class`, `ExecutionID`), tagged ones their tags.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Event {
    Classified(Classified),
    GrantObserved(GrantObserved),
    GrantCleared(GrantCleared),
    GateTargetRequested(GateTargetRequested),
    GateSatisfied(GateSatisfied),
    GateBlocked(GateBlocked),
    TargetRevoked(TargetRevoked),
    GatePassed(GatePassed),
    GateReleased(GateReleased),
    ClaimReleased(ClaimReleased),
    PRClosedRecorded(PRClosedRecorded),
    RunQueued(RunQueued),
    RunStarted(RunStarted),
    RunStartFailed(RunStartFailed),
    RunSuperseded(RunSuperseded),
    RunAdopted(RunAdopted),
    RunCompleted(RunCompleted),
    AdminGrantReleased(AdminGrantReleased),
    AdminExecutionCancelled(AdminExecutionCancelled),
    AdminGateSatisfied(AdminGateSatisfied),
    AdminCheckOverridden(AdminCheckOverridden),
}

wrap_variants!(Event: Classified, GrantObserved, GrantCleared, GateTargetRequested, GateSatisfied,
    GateBlocked, TargetRevoked, GatePassed, GateReleased, ClaimReleased, PRClosedRecorded,
    RunQueued, RunStarted, RunStartFailed, RunSuperseded, RunAdopted, RunCompleted,
    AdminGrantReleased, AdminExecutionCancelled, AdminGateSatisfied, AdminCheckOverridden);

// --- gate-lifecycle facts ---

/// Establishes or replaces the gate target set. Evolve carries forward still-live
/// prior grants; decide has already resolved the effective set.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct Classified {
    #[serde(rename = "Gates", default, deserialize_with = "nullable")]
    pub gates: Vec<GateTarget>,
}

#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct GrantObserved {
    #[serde(rename = "Class")]
    pub class: String,
    #[serde(rename = "Target")]
    pub target: String,
    #[serde(rename = "Name")]
    pub name: String,
    #[serde(rename = "State")]
    pub state: GrantState,
    #[serde(rename = "Requester")]
    pub requester: String,
}

#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct GrantCleared {
    #[serde(rename = "Class")]
    pub class: String,
    #[serde(rename = "Target")]
    pub target: String,
}

/// A target needs a grant. `requester` pins the lease.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct GateTargetRequested {
    #[serde(rename = "Class")]
    pub class: String,
    #[serde(rename = "Target")]
    pub target: String,
    #[serde(rename = "Requester")]
    pub requester: String,
}

#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct GateSatisfied {}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct GateBlocked {
    #[serde(rename = "Reason")]
    pub reason: BlockReason,
    #[serde(rename = "ByPR", default)]
    pub by_pr: i64,
    #[serde(rename = "ByEnv", default)]
    pub by_env: String,
}

impl GateBlocked {
    /// A block with only a reason.
    pub fn new(reason: BlockReason) -> GateBlocked {
        GateBlocked {
            reason,
            by_pr: 0,
            by_env: String::new(),
        }
    }
}

#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct TargetRevoked {
    #[serde(rename = "Class")]
    pub class: String,
    #[serde(rename = "Target")]
    pub target: String,
    #[serde(rename = "PR")]
    pub pr: i64,
    #[serde(rename = "Env")]
    pub env: String,
}

/// Folds the gate to `Clean` and renders success: plan clean, nothing to gate.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct GatePassed {}

/// Folds the gate to `Clean` and renders nothing: post-apply cleanup.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct GateReleased {}

// --- run-triggering facts ---

/// A run request was accepted. The execution id is minted deterministically by
/// decide from pr, env, kind, sha and attempt.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct RunQueued {
    #[serde(rename = "Kind")]
    pub kind: String,
    #[serde(rename = "SHA")]
    pub sha: String,
    #[serde(rename = "Branch")]
    pub branch: String,
    #[serde(rename = "ExecutionID")]
    pub execution_id: String,
    #[serde(rename = "Attempt")]
    pub attempt: i64,
}

/// The executor accepted the build.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct RunStarted {
    #[serde(rename = "Kind")]
    pub kind: String,
    #[serde(rename = "ExecutionID")]
    pub execution_id: String,
    #[serde(rename = "BuildRef")]
    pub build_ref: String,
}

/// The executor refused or errored: a terminal check failure.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct RunStartFailed {
    #[serde(rename = "Kind")]
    pub kind: String,
    #[serde(rename = "ExecutionID")]
    pub execution_id: String,
    #[serde(rename = "Reason")]
    pub reason: String,
}

/// A newer SHA replaced an in-flight plan run (applies are never superseded).
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct RunSuperseded {
    #[serde(rename = "Kind")]
    pub kind: String,
    #[serde(rename = "OldExecutionID")]
    pub old_execution_id: String,
    #[serde(rename = "OldBuildRef")]
    pub old_build_ref: String,
    #[serde(rename = "NewExecutionID")]
    pub new_execution_id: String,
    #[serde(rename = "NewSHA")]
    pub new_sha: String,
}

/// Serve bound a run to a build it did not launch. Unlike `RunQueued` it carries
/// the existing build ref and starts nothing.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct RunAdopted {
    #[serde(rename = "Kind")]
    pub kind: String,
    #[serde(rename = "ExecutionID")]
    pub execution_id: String,
    #[serde(rename = "SHA")]
    pub sha: String,
    #[serde(rename = "Branch")]
    pub branch: String,
    #[serde(rename = "Attempt")]
    pub attempt: i64,
    #[serde(rename = "BuildRef")]
    pub build_ref: String,
}

/// The runner took over and finalized: the run-start lifecycle is over.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct RunCompleted {
    #[serde(rename = "Kind")]
    pub kind: String,
    #[serde(rename = "ExecutionID")]
    pub execution_id: String,
}

// --- claim-ledger fact ---

#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct ClaimReleased {
    #[serde(rename = "PR")]
    pub pr: i64,
    #[serde(rename = "Environment")]
    pub environment: String,
}

// --- trigger fact ---

/// Marks a PR-closure teardown so react publishes SSE only, with no check run.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct PRClosedRecorded {}

// --- admin interventions ---

#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct AdminGrantReleased {
    pub pr: i64,
    pub environment: String,
    pub class: String,
    pub target: String,
    pub actor: String,
    pub reason: String,
}

#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct AdminExecutionCancelled {
    pub kind: String,
    pub execution_id: String,
    pub actor: String,
    pub reason: String,
}

#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct AdminGateSatisfied {
    pub pr: i64,
    pub environment: String,
    pub class: String,
    pub target: String,
    pub actor: String,
    pub reason: String,
}

#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct AdminCheckOverridden {
    pub pr: i64,
    pub env: String,
    pub check_name: String,
    pub conclusion: String,
    pub actor: String,
    pub reason: String,
}
