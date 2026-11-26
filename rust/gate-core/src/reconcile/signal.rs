use crate::approval::GrantState;
use crate::events::GateTarget;

use super::wrap_variants;

/// Everything that can drive a [`ChangeSet`](super::ChangeSet).
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Signal {
    RunnerFinalize(RunnerFinalize),
    PRClosed(PRClosed),
    GrantsObserved(GrantsObserved),
    GateTick(GateTick),
    ApplySucceeded(ApplySucceeded),
    RunRequested(RunRequested),
    RunStartResult(RunStartResult),
    InboundBuild(InboundBuild),
}

wrap_variants!(Signal: RunnerFinalize, PRClosed, GrantsObserved, GateTick, ApplySucceeded,
    RunRequested, RunStartResult, InboundBuild);

/// The gate half of a runner Finalize report: the (class, target) pairs that need
/// approval.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct RunnerFinalize {
    pub gates: Vec<GateTarget>,
    /// A finalize from the post-merge apply rather than the plan gate. It is a
    /// recovery signal: it may add or refresh targets but never weakens a gate the
    /// plan established, so decide unions the prior targets and never prunes.
    pub apply_context: bool,
}

/// The PR was closed.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct PRClosed {}

/// Grant observations for this change set: the results of `RequestGrant` actions,
/// or a re-list. Targets not mentioned are left unchanged.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct GrantsObserved {
    pub grants: Vec<ObservedGrant>,
}

/// The periodic backstop re-observation: a full re-list of the change set's
/// targets, so a target missing from it has lost its grant.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct GateTick {
    pub grants: Vec<ObservedGrant>,
}

/// The apply finished.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct ApplySucceeded {}

/// Asks for a CI run: a pull request push plans its head, a push to main applies
/// the merge commit, and a check re-run asks for the same kind again with `rerun`
/// set (a new attempt even for a SHA already seen).
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct RunRequested {
    pub kind: String,
    pub sha: String,
    pub branch: String,
    pub rerun: bool,
}

/// The executor's answer to a `StartRun` action: a build ref, or an error.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct RunStartResult {
    pub kind: String,
    pub execution_id: String,
    pub build_ref: String,
    pub err: String,
}

/// A build serve may not have launched (a native re-run or a console rebuild),
/// already correlated to this change set by the shell.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct InboundBuild {
    pub kind: String,
    /// The build's resolved commit SHA.
    pub sha: String,
    /// The executor's build id.
    pub build_ref: String,
}

/// One grant fact the shell gathered for a (class, target).
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct ObservedGrant {
    pub class: String,
    pub target: String,
    /// The backend-assigned id, empty if none.
    pub name: String,
    /// `Absent` when no grant exists for this change set.
    pub state: GrantState,
    /// The leased identity, empty if not applicable.
    pub requester: String,
    /// Set when a `RequestGrant` hit a slot collision.
    pub collision: Option<Collision>,
}

/// A slot blocker the shell already resolved against GitHub.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Collision {
    pub by_pr: i64,
    pub by_env: String,
    /// The blocker is this change set's own PR, in another environment.
    pub by_self: bool,
    /// The blocker's PR is closed without being merged.
    pub by_pr_abandoned: bool,
}

#[cfg(test)]
mod tests;
