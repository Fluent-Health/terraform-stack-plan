use super::{
    ChangeSet, Event, RUN_KIND_APPLY, RUN_KIND_PLAN, RunCompleted, RunPhase, RunQueued,
    RunRequested, RunStartFailed, RunStartResult, RunStarted, RunSuperseded,
};

/// A webhook-driven CI run request.
///
/// The same (kind, sha) without an explicit rerun is a webhook redelivery: no
/// events. A new SHA while a plan run is live supersedes it; a live apply is never
/// disturbed. A rerun, or a retry after a failed start, queues the same SHA again
/// with the next attempt.
pub(super) fn decide_run_requested(state: &ChangeSet, sig: &RunRequested) -> Vec<Event> {
    if sig.kind != RUN_KIND_PLAN && sig.kind != RUN_KIND_APPLY {
        return vec![];
    }
    if sig.sha.is_empty() {
        return vec![];
    }
    let prior = state.runs.get(&sig.kind);
    let mut attempt = 1;
    let mut evs: Vec<Event> = vec![];
    if let Some(prior) = prior {
        // A redelivery never re-triggers a build, whatever the run's phase; only the
        // Re-run button does.
        if prior.sha == sig.sha && !sig.rerun {
            return vec![];
        }
        // Attempts grow across every request, not per SHA: a force-push round trip
        // (A, B, A) must not re-mint A's first execution id.
        attempt = prior.attempt + 1;
    }
    let exec_id = run_execution_id(state.pr, &state.environment, &sig.kind, &sig.sha, attempt);
    if let Some(prior) = prior.filter(|p| p.live()) {
        if sig.kind == RUN_KIND_APPLY {
            // Never disturb a live apply; a request after it finishes queues normally.
            return vec![];
        }
        evs.push(
            RunSuperseded {
                kind: sig.kind.clone(),
                old_execution_id: prior.execution_id.clone(),
                old_build_ref: prior.build_ref.clone(),
                new_execution_id: exec_id.clone(),
                new_sha: sig.sha.clone(),
            }
            .into(),
        );
    }
    evs.push(
        RunQueued {
            kind: sig.kind.clone(),
            sha: sig.sha.clone(),
            branch: sig.branch.clone(),
            execution_id: exec_id,
            attempt,
        }
        .into(),
    );
    evs
}

/// Closes the run-start lifecycle when the runner finalizes: the run of the
/// matching kind completes if it is live or failed to start (a client-side start
/// timeout whose build ran anyway). The finalize proves the build ran.
pub(super) fn run_completion_events(state: &ChangeSet, apply_context: bool) -> Vec<Event> {
    let kind = if apply_context {
        RUN_KIND_APPLY
    } else {
        RUN_KIND_PLAN
    };
    match state.runs.get(kind) {
        Some(r) if r.live() || r.phase == RunPhase::StartFailed => vec![
            RunCompleted {
                kind: kind.to_string(),
                execution_id: r.execution_id.clone(),
            }
            .into(),
        ],
        _ => vec![],
    }
}

/// Folds the executor's answer to a `StartRun` action. Feedback for another
/// execution id than the current run's is stale (a supersede raced the start) and
/// dropped.
pub(super) fn decide_run_start_result(state: &ChangeSet, sig: &RunStartResult) -> Vec<Event> {
    let Some(r) = state
        .runs
        .get(&sig.kind)
        .filter(|r| r.execution_id == sig.execution_id)
    else {
        return vec![];
    };
    if !sig.err.is_empty() {
        if r.phase == RunPhase::StartFailed {
            return vec![];
        }
        return vec![
            RunStartFailed {
                kind: sig.kind.clone(),
                execution_id: sig.execution_id.clone(),
                reason: sig.err.clone(),
            }
            .into(),
        ];
    }
    if r.phase == RunPhase::Started && r.build_ref == sig.build_ref {
        return vec![];
    }
    vec![
        RunStarted {
            kind: sig.kind.clone(),
            execution_id: sig.execution_id.clone(),
            build_ref: sig.build_ref.clone(),
        }
        .into(),
    ]
}

/// Marks serve-minted run execution ids. The store's stuck-run query and the queued
/// check render tell serve-initiated runs from runner-created executions by it.
pub const RUN_EXECUTION_ID_PREFIX: &str = "run-";

/// Whether `id` is a serve-minted run execution id (`run-<pr>-<env>-<kind>-<sha12>-a<attempt>`).
pub fn is_run_execution_id(id: &str) -> bool {
    id.starts_with(RUN_EXECUTION_ID_PREFIX)
}

/// The deterministic execution id of a run: decide is pure, so the id is a
/// function of the request. The attempt keeps rerun ids unique.
pub(super) fn run_execution_id(pr: i64, env: &str, kind: &str, sha: &str, attempt: i64) -> String {
    // Go slices bytes; a SHA is ASCII, and `get` keeps a non-ASCII input total.
    let short = sha.get(..12).unwrap_or(sha);
    format!("run-{pr}-{env}-{kind}-{short}-a{attempt}")
}

#[cfg(test)]
mod tests;
