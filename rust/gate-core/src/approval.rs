//! The approval-gate types (Go: `internal/approval`). The backend trait that requests,
//! lists and revokes grants does I/O and belongs to the serve layer; the state a gate
//! folds over lives here.

use serde::{Deserialize, Deserializer, Serialize, Serializer};

/// The lifecycle of an approval grant, normalised across backends. `Absent` is Go's
/// empty string: no grant for this target. A backend state outside the normalised
/// set passes through as `Other`, as Go's PAM backend passes it through unchanged
/// (for example `ACTIVATION_FAILED`): it is closed, and it does not block a gate.
#[derive(Debug, Clone, Default, PartialEq, Eq, Hash)]
pub enum GrantState {
    #[default]
    Absent,
    /// Requested; awaiting an approver.
    Awaiting,
    /// Approved; becoming active.
    Activating,
    /// Approved and active.
    Active,
    Denied,
    Revoked,
    Expired,
    /// A backend state outside the set above, kept as written.
    Other(String),
}

impl GrantState {
    /// Whether the grant is live and not terminal.
    pub fn open(&self) -> bool {
        matches!(
            self,
            GrantState::Awaiting | GrantState::Activating | GrantState::Active
        )
    }

    /// The stored string, as Go writes it.
    pub fn as_str(&self) -> &str {
        match self {
            GrantState::Absent => "",
            GrantState::Awaiting => "AWAITING",
            GrantState::Activating => "ACTIVATING",
            GrantState::Active => "ACTIVE",
            GrantState::Denied => "DENIED",
            GrantState::Revoked => "REVOKED",
            GrantState::Expired => "EXPIRED",
            GrantState::Other(s) => s,
        }
    }

    /// The state a stored string names.
    pub fn parse(raw: &str) -> GrantState {
        match raw {
            "" => GrantState::Absent,
            "AWAITING" => GrantState::Awaiting,
            "ACTIVATING" => GrantState::Activating,
            "ACTIVE" => GrantState::Active,
            "DENIED" => GrantState::Denied,
            "REVOKED" => GrantState::Revoked,
            "EXPIRED" => GrantState::Expired,
            other => GrantState::Other(other.to_string()),
        }
    }
}

impl Serialize for GrantState {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        serializer.serialize_str(self.as_str())
    }
}

impl<'de> Deserialize<'de> for GrantState {
    fn deserialize<D: Deserializer<'de>>(deserializer: D) -> Result<Self, D::Error> {
        Ok(GrantState::parse(&String::deserialize(deserializer)?))
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
