//! The server's runtime configuration (Go: `server.Config`, and the progress and
//! scope pieces of `internal/config`).

use std::collections::HashMap;

use tfstackplan_gate::events::Phase;

/// The CI runner: execution lifecycle events, logs, gates, claims.
pub const SCOPE_REPORT: &str = "report";
/// Read-only: execution state and events, claims listing.
pub const SCOPE_READ: &str = "read";
/// Operator surgery: claim release and the admin verbs.
pub const SCOPE_ADMIN: &str = "admin";

/// One phase's slice of the progress bar.
#[derive(Debug, Clone, PartialEq)]
pub struct PhaseWeight {
    pub phase: Phase,
    pub weight: f64,
}

/// The ordered weighted phases per operation (the policy file's `progress {}`).
#[derive(Debug, Clone, Default, PartialEq)]
pub struct ProgressConfig {
    pub plan: Vec<PhaseWeight>,
    pub apply: Vec<PhaseWeight>,
}

impl ProgressConfig {
    /// The phases of `kind` ("plan" or "apply").
    pub fn for_kind(&self, kind: &str) -> &[PhaseWeight] {
        if kind == "apply" {
            &self.apply
        } else {
            &self.plan
        }
    }
}

/// The weight a phase gets when the config names none.
pub fn default_phase_weight(p: &str) -> f64 {
    match p {
        Phase::INITIALIZING | Phase::TESTING => 2.0,
        Phase::PLANNING | Phase::APPLYING => 10.0,
        _ => 1.0,
    }
}

/// The server runtime configuration.
#[derive(Debug, Clone, Default)]
pub struct Config {
    /// This serve's public origin, for Details links and the OIDC audience. Empty
    /// derives it from the request.
    pub public_base_url: String,
    /// The central UI's base URL; check-run Details links point there.
    pub ui_base_url: String,
    /// This serve's tier. Empty disables run triggering.
    pub environment: String,
    /// Where per-stack log buffers live. Empty disables log ingestion.
    pub logs_dir: String,
    /// The only identity allowed to push to `/pubsub/*`; empty accepts any verified
    /// token.
    pub push_service_account: String,
    /// Verified caller email (lowercase) to the scopes it holds.
    pub api_principals: HashMap<String, Vec<String>>,
    /// GitHub's webhook HMAC secret. Empty disables `/github/webhook`.
    pub github_webhook_secret: String,
    /// The weighted progress phases; `None` uses the built-in fractions.
    pub progress: Option<ProgressConfig>,
    /// Cloud Build trigger name to run kind, for inbound build reconciliation.
    pub build_trigger_names: HashMap<String, String>,
    /// The approval requester pool identities.
    pub requester_pool: Vec<String>,
}
