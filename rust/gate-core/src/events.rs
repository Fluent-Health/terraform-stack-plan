//! The typed protocol the CI runner speaks to the server (Go: `internal/events`):
//! the lifecycle of a multi-stack Terraform execution (init, phase, per-stack
//! updates, finalize) and the apply-time gate exchange. Field names on the wire are
//! Go's JSON tags.

use std::collections::BTreeMap;
use std::fmt;

use jiff::Timestamp;
use serde::{Deserialize, Deserializer, Serialize, Serializer};

pub use crate::domain::{Category, Counts};
use crate::json::nullable;

/// The protocol version. Runner and server share it.
pub const VERSION: i64 = 1;

/// The lifecycle of one stack within an execution. `Unset` is Go's empty string: a
/// field that was never set, which decodes cleanly.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, Hash)]
pub enum Status {
    #[default]
    Unset,
    Pending,
    Initializing,
    Initialized,
    Running,
    Planned,
    Gated,
    Safe,
    Nochange,
    Moving,
    Failed,
    Aborted,
}

/// A status string outside the known set (Go's coded `WIRE-001` error).
#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
#[error("WIRE-001: unknown stack status {0:?}")]
pub struct UnknownStatus(pub String);

impl Status {
    /// Every known status except `Unset`.
    pub const ALL: [Status; 11] = [
        Status::Pending,
        Status::Initializing,
        Status::Initialized,
        Status::Running,
        Status::Planned,
        Status::Gated,
        Status::Safe,
        Status::Nochange,
        Status::Moving,
        Status::Failed,
        Status::Aborted,
    ];

    /// The wire string.
    pub fn as_str(self) -> &'static str {
        match self {
            Status::Unset => "",
            Status::Pending => "pending",
            Status::Initializing => "initializing",
            Status::Initialized => "initialized",
            Status::Running => "running",
            Status::Planned => "planned",
            Status::Gated => "gated",
            Status::Safe => "safe",
            Status::Nochange => "nochange",
            Status::Moving => "moving",
            Status::Failed => "failed",
            Status::Aborted => "aborted",
        }
    }

    /// Parses a wire string; the empty string is `Unset`.
    pub fn parse(raw: &str) -> Result<Status, UnknownStatus> {
        if raw.is_empty() {
            return Ok(Status::Unset);
        }
        Status::ALL
            .into_iter()
            .find(|s| s.as_str() == raw)
            .ok_or_else(|| UnknownStatus(raw.to_string()))
    }
}

impl fmt::Display for Status {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(self.as_str())
    }
}

impl Serialize for Status {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        serializer.serialize_str(self.as_str())
    }
}

impl<'de> Deserialize<'de> for Status {
    fn deserialize<D: Deserializer<'de>>(deserializer: D) -> Result<Self, D::Error> {
        let raw = String::deserialize(deserializer)?;
        Status::parse(&raw).map_err(serde::de::Error::custom)
    }
}

/// The execution-wide lifecycle phase. Free text on the wire; the known phases are
/// the associated constants.
#[derive(Debug, Clone, Default, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(transparent)]
pub struct Phase(pub String);

impl Phase {
    pub const WARMING: &'static str = "warming";
    pub const LINTING: &'static str = "linting";
    pub const INITIALIZING: &'static str = "initializing";
    pub const PLANNING: &'static str = "planning";
    pub const APPLYING: &'static str = "applying";
    pub const TESTING: &'static str = "testing";
    pub const VERIFYING: &'static str = "verifying";
    pub const IMAGE: &'static str = "image";
    pub const CLAIM: &'static str = "claim";
    pub const MOVES: &'static str = "moves";
    pub const CLASSIFY: &'static str = "classify";
    pub const REPORT: &'static str = "report";

    /// A phase from a string.
    pub fn new(phase: &str) -> Phase {
        Phase(phase.to_string())
    }

    /// Whether the phase advances per stack (k/N sub-progress) rather than being an
    /// instant marker.
    pub fn ticking(&self) -> bool {
        matches!(
            self.0.as_str(),
            Phase::INITIALIZING | Phase::PLANNING | Phase::APPLYING
        )
    }

    /// Whether the phase is set.
    pub fn valid(&self) -> bool {
        !self.0.is_empty()
    }

    /// The phase as a string.
    pub fn as_str(&self) -> &str {
        &self.0
    }
}

/// One node in the execution graph.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct StackState {
    pub path: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub project: String,
    #[serde(default, skip_serializing_if = "is_unset")]
    pub status: Status,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub detail: String,
    #[serde(
        default,
        deserialize_with = "nullable",
        skip_serializing_if = "Vec::is_empty"
    )]
    pub categories: Vec<Category>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub counts: Option<Counts>,
}

fn is_unset(s: &Status) -> bool {
    *s == Status::Unset
}

/// A dependency: `from` runs before `to`.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct Edge {
    pub from: String,
    pub to: String,
}

/// The changed subgraph of one execution.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct Graph {
    #[serde(default, deserialize_with = "nullable")]
    pub stacks: Vec<StackState>,
    #[serde(default, deserialize_with = "nullable")]
    pub edges: Vec<Edge>,
}

/// One thing that must be approved: a classification class against a target.
#[derive(Debug, Clone, Default, PartialEq, Eq, PartialOrd, Ord, Hash, Serialize, Deserialize)]
pub struct GateTarget {
    pub class: String,
    pub target: String,
}

/// Registers an execution and its changed subgraph.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Init {
    pub id: String,
    pub repo: String,
    pub sha: String,
    pub pr: i64,
    pub environment: String,
    pub log_url: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub context: String,
    #[serde(deserialize_with = "nullable")]
    pub stacks: Vec<StackState>,
    #[serde(deserialize_with = "nullable")]
    pub edges: Vec<Edge>,
}

/// Narrates a lifecycle phase transition (may arrive before `Init`).
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct PhaseEvent {
    pub id: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub repo: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub sha: String,
    #[serde(skip_serializing_if = "crate::json::is_zero")]
    pub pr: i64,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub environment: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub log_url: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub context: String,
    pub phase: Phase,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub label: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub progress_pct: Option<i64>,
}

/// A slice of one stack's combined output.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct LogChunk {
    pub id: String,
    pub stack: String,
    pub data: String,
}

/// Ticks one stack's status.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Update {
    pub id: String,
    pub stack: String,
    pub status: Status,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub detail: String,
}

/// A stack's Terramate watch or causality trigger reason.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct ChangeReason {
    pub stack: String,
    /// direct, watch, module or unknown.
    pub kind: String,
    #[serde(deserialize_with = "nullable")]
    pub via: Vec<String>,
}

/// The terminal plan state: the rendered report, per-stack target backfill, the
/// gates that must be approved, and the moving and failed flags.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Finalize {
    pub id: String,
    pub report_markdown: String,
    #[serde(
        deserialize_with = "nullable",
        skip_serializing_if = "BTreeMap::is_empty"
    )]
    pub projects: BTreeMap<String, String>,
    #[serde(
        deserialize_with = "nullable",
        skip_serializing_if = "BTreeMap::is_empty"
    )]
    pub stack_reports: BTreeMap<String, String>,
    #[serde(deserialize_with = "nullable", skip_serializing_if = "Vec::is_empty")]
    pub gates: Vec<GateTarget>,
    #[serde(deserialize_with = "nullable", skip_serializing_if = "Vec::is_empty")]
    pub moving: Vec<String>,
    #[serde(skip_serializing_if = "std::ops::Not::not")]
    pub failed: bool,
    #[serde(
        deserialize_with = "nullable",
        skip_serializing_if = "BTreeMap::is_empty"
    )]
    pub categories: BTreeMap<String, Vec<Category>>,
    #[serde(
        deserialize_with = "nullable",
        skip_serializing_if = "BTreeMap::is_empty"
    )]
    pub counts: BTreeMap<String, Counts>,
    #[serde(deserialize_with = "nullable", skip_serializing_if = "Vec::is_empty")]
    pub change_reasons: Vec<ChangeReason>,
}

/// The apply-time gate pre-check: is every required gate for this (PR,
/// environment) approved?
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct GateCheck {
    pub pr: i64,
    pub environment: String,
}

/// Asks the server to revoke the grants it requested for this (PR, environment).
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct GateRevoke {
    pub pr: i64,
    pub environment: String,
}

/// One apply-lock claim row.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Claim {
    pub environment: String,
    pub stack_path: String,
    pub owner_pr: i64,
    pub expires_at: Timestamp,
}

#[cfg(test)]
mod tests;
