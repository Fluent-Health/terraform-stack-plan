use std::collections::BTreeMap;

use jiff::{SignedDuration, Timestamp};
use serde::{Deserialize, Serialize};

use crate::json::nullable;
use crate::reconcile::wrap_variants;

/// How long a claim holds before it must be renewed.
pub const LEASE: SignedDuration = SignedDuration::from_mins(30);

/// The owner of one stack slot.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
pub struct Claim {
    #[serde(rename = "PR")]
    pub pr: i64,
    #[serde(rename = "ExpiresAt")]
    pub expires_at: Timestamp,
}

/// The folded ledger: stack path to claim.
pub type ClaimSet = BTreeMap<String, Claim>;

/// A request to change the ledger.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Command {
    AcquireClaim(AcquireClaim),
    RenewClaim(RenewClaim),
    ReleaseClaim(ReleaseClaim),
    ReleaseClaimStack(ReleaseClaimStack),
}

wrap_variants!(Command: AcquireClaim, RenewClaim, ReleaseClaim, ReleaseClaimStack);

/// `pr` claims `stacks`, as of `now`.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct AcquireClaim {
    pub pr: i64,
    pub stacks: Vec<String>,
    pub now: Timestamp,
}

/// Extends `pr`'s claims, as of `now`.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct RenewClaim {
    pub pr: i64,
    pub now: Timestamp,
}

/// Releases every stack `pr` claims.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ReleaseClaim {
    pub pr: i64,
}

/// Releases one stack `pr` claims.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ReleaseClaimStack {
    pub pr: i64,
    pub stack: String,
}

/// A past-tense fact produced by [`decide`](super::decide) and folded by
/// [`evolve`](super::evolve).
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Event {
    ClaimAcquired(ClaimAcquired),
    ClaimRenewed(ClaimRenewed),
    ClaimReleased(ClaimReleased),
    ClaimStackReleased(ClaimStackReleased),
}

wrap_variants!(Event: ClaimAcquired, ClaimRenewed, ClaimReleased, ClaimStackReleased);

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct ClaimAcquired {
    #[serde(rename = "PR", default)]
    pub pr: i64,
    #[serde(rename = "Stacks", default, deserialize_with = "nullable")]
    pub stacks: Vec<String>,
    #[serde(rename = "ExpiresAt")]
    pub expires_at: Timestamp,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct ClaimRenewed {
    #[serde(rename = "PR", default)]
    pub pr: i64,
    #[serde(rename = "ExpiresAt")]
    pub expires_at: Timestamp,
}

#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct ClaimReleased {
    #[serde(rename = "PR")]
    pub pr: i64,
}

#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct ClaimStackReleased {
    #[serde(rename = "PR")]
    pub pr: i64,
    #[serde(rename = "Stack")]
    pub stack: String,
}
