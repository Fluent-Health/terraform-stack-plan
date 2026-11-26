use super::decide_run::run_execution_id;
use super::{
    ChangeSet, Event, InboundBuild, RUN_KIND_APPLY, RUN_KIND_PLAN, RunAdopted, RunSuperseded,
};

/// Reconciles a build serve did not launch onto the PR's stuck run. It fires only
/// for a new build id on the current run's commit after serve gave up on that run
/// (start failed, completed or superseded): a live run is left alone, and a build
/// serve already tracks is a no-op. The stuck run's execution is superseded and the
/// build adopted under a fresh execution id with the next attempt.
pub(super) fn decide_inbound_build(state: &ChangeSet, sig: &InboundBuild) -> Vec<Event> {
    if sig.kind != RUN_KIND_PLAN && sig.kind != RUN_KIND_APPLY {
        return vec![];
    }
    if sig.build_ref.is_empty() || sig.sha.is_empty() {
        return vec![];
    }
    let Some(r) = state.runs.get(&sig.kind) else {
        return vec![];
    };
    if r.build_ref == sig.build_ref || r.sha != sig.sha || r.live() {
        return vec![];
    }
    let attempt = r.attempt + 1;
    let new_id = run_execution_id(state.pr, &state.environment, &sig.kind, &sig.sha, attempt);
    vec![
        RunSuperseded {
            kind: sig.kind.clone(),
            old_execution_id: r.execution_id.clone(),
            old_build_ref: r.build_ref.clone(),
            new_execution_id: new_id.clone(),
            new_sha: sig.sha.clone(),
        }
        .into(),
        RunAdopted {
            kind: sig.kind.clone(),
            execution_id: new_id,
            sha: sig.sha.clone(),
            branch: r.branch.clone(),
            attempt,
            build_ref: sig.build_ref.clone(),
        }
        .into(),
    ]
}
