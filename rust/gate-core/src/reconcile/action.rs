use super::wrap_variants;

/// An effect the pure core wants performed but cannot do itself.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Action {
    RequestGrant(RequestGrant),
    RevokeGrant(RevokeGrant),
    RenderCheckRun(RenderCheckRun),
    PostCommitStatus(PostCommitStatus),
    PublishSSE(PublishSSE),
    ReleaseClaim(ReleaseClaim),
    StartRun(StartRun),
    CancelRun(CancelRun),
    AdoptRun(AdoptRun),
}

wrap_variants!(Action: RequestGrant, RevokeGrant, RenderCheckRun, PostCommitStatus, PublishSSE,
    ReleaseClaim, StartRun, CancelRun, AdoptRun);

/// Asks the backend to ensure an open grant. Its result (grant name and leased
/// requester, or a collision) comes back as a `GrantsObserved` signal. `requester`
/// pins the lease; empty lets the backend lease.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct RequestGrant {
    pub class: String,
    pub target: String,
    pub requester: String,
}

/// Revokes the open grant for (class, target, PR, environment). Idempotent. The PR
/// and environment are set explicitly when revoking a foreign slot blocker.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct RevokeGrant {
    pub class: String,
    pub target: String,
    pub pr: i64,
    pub environment: String,
}

/// Re-renders the GitHub check run from current state.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct RenderCheckRun {
    pub terminal: bool,
    /// `success`, `action_required`, `failure`, or empty while in progress.
    pub conclusion: String,
}

/// Posts the commit status.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct PostCommitStatus {
    /// `success`, `failure` or `pending`.
    pub state: String,
    pub description: String,
}

/// Tells the live page's hub that state changed.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct PublishSSE {}

/// Asks the executor to start a CI build. Its result comes back as a
/// `RunStartResult` signal. The shell first creates the execution row and a
/// queued check run.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct StartRun {
    pub kind: String,
    pub sha: String,
    pub branch: String,
    pub execution_id: String,
}

/// Supersedes an in-flight run: the store marks the old execution superseded by
/// the new one, and the old build is cancelled when it has a build ref.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct CancelRun {
    pub kind: String,
    pub old_execution_id: String,
    pub old_build_ref: String,
    pub new_execution_id: String,
}

/// Materializes the execution row and check run for a build serve did not launch.
/// Never calls the executor: the build already exists.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct AdoptRun {
    pub kind: String,
    pub execution_id: String,
    pub sha: String,
    pub build_ref: String,
}

/// Releases the merge-lock claim a PR holds in an environment, after its apply.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct ReleaseClaim {
    pub pr: i64,
    pub environment: String,
}

#[cfg(test)]
mod tests;
