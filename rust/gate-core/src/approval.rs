//! The approval-gate types (Go: `internal/approval`). The backend trait that requests,
//! lists and revokes grants does I/O and belongs to the serve layer; the state a gate
//! folds over lives here.

use serde::{Deserialize, Serialize};

/// The lifecycle of an approval grant, normalised across backends. `Absent` is Go's
/// empty string: no grant for this target.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, Hash, Serialize, Deserialize)]
pub enum GrantState {
    #[default]
    #[serde(rename = "")]
    Absent,
    /// Requested; awaiting an approver.
    #[serde(rename = "AWAITING")]
    Awaiting,
    /// Approved; becoming active.
    #[serde(rename = "ACTIVATING")]
    Activating,
    /// Approved and active.
    #[serde(rename = "ACTIVE")]
    Active,
    #[serde(rename = "DENIED")]
    Denied,
    #[serde(rename = "REVOKED")]
    Revoked,
    #[serde(rename = "EXPIRED")]
    Expired,
}

impl GrantState {
    /// Whether the grant is live and not terminal.
    pub fn open(self) -> bool {
        matches!(
            self,
            GrantState::Awaiting | GrantState::Activating | GrantState::Active
        )
    }

    /// The stored string, as Go writes it.
    pub fn as_str(self) -> &'static str {
        match self {
            GrantState::Absent => "",
            GrantState::Awaiting => "AWAITING",
            GrantState::Activating => "ACTIVATING",
            GrantState::Active => "ACTIVE",
            GrantState::Denied => "DENIED",
            GrantState::Revoked => "REVOKED",
            GrantState::Expired => "EXPIRED",
        }
    }
}

/// Correlates a gate to a change: a class against a target, for one PR and
/// environment.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Request {
    pub class: String,
    pub target: String,
    pub pr: i64,
    pub environment: String,
    /// An optional pre-leased pool identity; empty lets the backend lease one.
    pub requester: String,
}

/// A backend's record of an approval grant.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Grant {
    /// The backend-assigned name or id.
    pub name: String,
    pub state: GrantState,
    /// The correlation, parsed back out of the grant by the backend.
    pub request: Request,
    /// The requesting identity, where the backend pools requesters.
    pub requester: String,
}

/// A grant request failed because an open grant for another (PR, environment)
/// already holds the requester's slot on this entitlement.
#[derive(Debug, Clone, Default, PartialEq, Eq, thiserror::Error)]
#[error("grant slot occupied by PR #{} env={}", .blocking_grant.request.pr, .blocking_grant.request.environment)]
pub struct SlotCollisionError {
    /// The open grant occupying the slot.
    pub blocking_grant: Grant,
}

#[cfg(test)]
mod tests;
