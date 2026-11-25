use crate::approval::GrantState;

use super::decide::prior_targets;
use super::{
    Blocked, Blocker, ChangeSet, CheckOverride, Event, GateState, Pending, Run, RunPhase,
    Satisfied, Target,
};

/// Folds one event into the change set. Total (every state and event pair has a
/// result), pure and deterministic.
pub fn evolve(mut cs: ChangeSet, e: &Event) -> ChangeSet {
    match e {
        // --- run-triggering facts ---
        Event::RunQueued(ev) => {
            cs.runs.insert(
                ev.kind.clone(),
                Run {
                    execution_id: ev.execution_id.clone(),
                    kind: ev.kind.clone(),
                    sha: ev.sha.clone(),
                    branch: ev.branch.clone(),
                    attempt: ev.attempt,
                    build_ref: String::new(),
                    phase: RunPhase::Queued,
                },
            );
        }
        Event::RunStarted(ev) => {
            // A stale start (a supersede raced it) does not fold.
            if let Some(r) = current_run(&mut cs, &ev.kind, &ev.execution_id) {
                r.build_ref = ev.build_ref.clone();
                r.phase = RunPhase::Started;
            }
        }
        Event::RunStartFailed(ev) => {
            if let Some(r) = current_run(&mut cs, &ev.kind, &ev.execution_id) {
                r.phase = RunPhase::StartFailed;
            }
        }
        Event::RunCompleted(ev) => {
            if let Some(r) = current_run(&mut cs, &ev.kind, &ev.execution_id) {
                r.phase = RunPhase::Completed;
            }
        }
        Event::RunSuperseded(ev) => {
            // Usually followed in the same batch by the RunQueued that replaces the
            // entry; folding superseded first keeps replays total.
            if let Some(r) = current_run(&mut cs, &ev.kind, &ev.old_execution_id) {
                r.phase = RunPhase::Superseded;
            }
        }
        Event::RunAdopted(ev) => {
            // Bound to a build that exists: created already started, replacing any
            // prior entry for the kind.
            cs.runs.insert(
                ev.kind.clone(),
                Run {
                    execution_id: ev.execution_id.clone(),
                    kind: ev.kind.clone(),
                    sha: ev.sha.clone(),
                    branch: ev.branch.clone(),
                    attempt: ev.attempt,
                    build_ref: ev.build_ref.clone(),
                    phase: RunPhase::Started,
                },
            );
        }

        // --- gate-lifecycle facts ---
        Event::Classified(ev) => {
            let prior = prior_targets(&cs.gate);
            let lease = cs.gate.lease();
            // Carry forward still-live prior grants: a re-classify never re-requests
            // a valid grant. Anything else starts clean so the request loop re-arms it.
            let targets = ev
                .gates
                .iter()
                .map(|g| match prior.get(&format!("{}|{}", g.class, g.target)) {
                    Some(pt) if pt.grant.open() => pt.clone(),
                    _ => Target {
                        class: g.class.clone(),
                        target: g.target.clone(),
                        ..Target::default()
                    },
                })
                .collect();
            cs.gate = GateState::Pending(Pending { targets, lease });
        }
        Event::GrantObserved(ev) => {
            if let Some(targets) = cs.gate.targets() {
                let mut targets = targets.to_vec();
                let mut lease = cs.gate.lease();
                for t in targets
                    .iter_mut()
                    .filter(|t| t.class == ev.class && t.target == ev.target)
                {
                    if !ev.name.is_empty() {
                        t.grant_name = ev.name.clone();
                    }
                    t.grant = ev.state;
                    if lease.requester.is_empty() && !ev.requester.is_empty() && ev.state.open() {
                        lease.requester = ev.requester.clone();
                    }
                }
                cs.gate = cs.gate.with_targets_and_lease(targets, lease);
            }
        }
        Event::GrantCleared(ev) => {
            cs.gate = set_grant(cs.gate, &ev.class, &ev.target, GrantState::Absent);
        }
        Event::GateTargetRequested(_) => {
            // No state change: the request is an effect, and the target keeps an
            // empty grant name until the backend answers.
        }
        Event::GateSatisfied(_) => {
            cs.gate = GateState::Satisfied(Satisfied {
                targets: cs.gate.targets().unwrap_or_default().to_vec(),
                lease: cs.gate.lease(),
            });
        }
        Event::GateBlocked(ev) => {
            cs.gate = GateState::Blocked(Blocked {
                targets: cs.gate.targets().unwrap_or_default().to_vec(),
                lease: cs.gate.lease(),
                by: Blocker {
                    reason: ev.reason,
                    by_pr: ev.by_pr,
                    by_env: ev.by_env.clone(),
                },
            });
        }
        Event::TargetRevoked(ev) => {
            // A foreign revoke (another PR's slot blocker) matches by class and
            // target only, as Go does; the foreign grant lives in its own change set.
            cs.gate = set_grant(cs.gate, &ev.class, &ev.target, GrantState::Revoked);
        }
        Event::AdminGrantReleased(ev) => {
            cs.gate = set_grant(cs.gate, &ev.class, &ev.target, GrantState::Revoked);
        }
        Event::AdminExecutionCancelled(_) => {
            // Audit only: the accompanying RunCompleted moves the run.
        }
        Event::AdminGateSatisfied(ev) => {
            cs.gate = set_grant(cs.gate, &ev.class, &ev.target, GrantState::Active);
        }
        Event::AdminCheckOverridden(ev) => {
            cs.check_override = Some(CheckOverride {
                check_name: ev.check_name.clone(),
                conclusion: ev.conclusion.clone(),
                actor: ev.actor.clone(),
                reason: ev.reason.clone(),
            });
        }
        Event::GatePassed(_) | Event::GateReleased(_) => cs.gate = GateState::Clean,

        // The claim ledger lives in its own aggregate; PRClosedRecorded is a
        // presentation trigger only.
        Event::ClaimReleased(_) | Event::PRClosedRecorded(_) => {}
    }
    cs
}

/// The run of `kind`, if its execution id is `execution_id`.
fn current_run<'a>(cs: &'a mut ChangeSet, kind: &str, execution_id: &str) -> Option<&'a mut Run> {
    cs.runs
        .get_mut(kind)
        .filter(|r| r.execution_id == execution_id)
}

/// Sets the grant of every target matching (class, target), keeping the variant.
fn set_grant(gate: GateState, class: &str, target: &str, grant: GrantState) -> GateState {
    let Some(targets) = gate.targets() else {
        return gate;
    };
    let mut targets = targets.to_vec();
    for t in targets
        .iter_mut()
        .filter(|t| t.class == class && t.target == target)
    {
        t.grant = grant;
    }
    gate.with_targets(targets)
}

#[cfg(test)]
pub(super) mod tests;
