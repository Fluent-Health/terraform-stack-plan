//! The per-environment value duplication lint (Go's `internal/uniqueness`):
//! flatten each environment's inputs to dot paths, decide which leaves look like
//! identifiers, find values shared across a tier boundary or leaking another
//! environment's token, and weigh them against reviewed `allow` exceptions.
//!
//! Pure: the shell walks the tree and reads the manifests, then hands their text
//! to [`source::load_unit`]; "today" is passed in as a [`Date`].

use std::collections::BTreeMap;

pub mod allow;
pub mod classify;
pub mod detect;
pub mod evaluate;
pub mod flatten;
pub mod report;
pub mod source;
pub mod value;
pub mod yaml;

pub use allow::{Date, allow_matches, partition};
pub use classify::{
    Classifier, boundary_token_pattern, default_key_patterns, default_quantity_suffixes,
    default_value_patterns, derive_tokens_and_segs,
};
pub use detect::{find_duplicates, find_env_tokens};
pub use evaluate::evaluate;
pub use flatten::flatten;
pub use value::Value;

/// An environment's protection class (`prod`, `nonprod`, …).
pub type Tier = String;

/// One instance manifest: its id and each environment's flattened inputs.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Unit {
    /// Root-relative path without the glob prefix and `.tm.yml`.
    pub id: String,
    /// The environment names found, sorted.
    pub envs: Vec<String>,
    /// `inputs[env][dot.path]`.
    pub inputs: BTreeMap<String, BTreeMap<String, Value>>,
}

/// Whether a finding fails the command.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Severity {
    /// Blocking.
    Violation,
    /// Reported, never blocking.
    ReportOnly,
}

impl Severity {
    /// The Go string.
    pub fn as_str(self) -> &'static str {
        match self {
            Severity::Violation => "violation",
            Severity::ReportOnly => "report-only",
        }
    }
}

/// Which detector found it.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Kind {
    /// The same identifier-shaped value in several environments.
    Duplicate,
    /// Another environment's token inside a value.
    EnvToken,
}

impl Kind {
    /// The Go string.
    pub fn as_str(self) -> &'static str {
        match self {
            Kind::Duplicate => "duplicate",
            Kind::EnvToken => "env-token",
        }
    }
}

/// One finding.
#[derive(Clone, Debug, PartialEq)]
pub struct Violation {
    /// The unit id.
    pub unit: String,
    /// The flattened key.
    pub key: String,
    /// The shared (or leaking) value.
    pub value: Value,
    /// The environments involved, sorted.
    pub envs: Vec<String>,
    /// The detector.
    pub kind: Kind,
    /// Blocking or not.
    pub severity: Severity,
}

/// A reviewed exception (`allow {}`).
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct AllowRule {
    /// Unit id.
    pub unit: String,
    /// Key, or a `filepath.Match` glob of keys.
    pub key: String,
    /// Environments covered.
    pub envs: Vec<String>,
    /// Justification.
    pub reason: String,
    /// `YYYY-MM-DD` expiry, or empty.
    pub expires: String,
}

/// The outcome of a run.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Report {
    /// Blocking findings no active allow covers.
    pub unjustified: Vec<Violation>,
    /// Active allows that matched nothing.
    pub stale: Vec<AllowRule>,
    /// Non-blocking findings.
    pub report_only: Vec<Violation>,
}
