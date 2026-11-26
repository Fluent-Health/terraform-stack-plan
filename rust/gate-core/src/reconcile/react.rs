use super::{
    Action, AdoptRun, BlockReason, CancelRun, ChangeSet, Event, GateState, PublishSSE,
    ReleaseClaim, RenderCheckRun, RequestGrant, RevokeGrant, StartRun,
};

/// The effects the shell must perform for the events of one decide. Presentation
/// (`RenderCheckRun`, `PublishSSE`) is derived here and never stored as an event.
/// `state` is the change set after folding `evs`.
pub fn react(state: &ChangeSet, evs: &[Event]) -> Vec<Action> {
    let mut actions: Vec<Action> = vec![];

    // Presentation precedence: 0 none, 1 in progress, 2 success or action_required,
    // 3 failure. The highest wins; one RenderCheckRun and PublishSSE at most.
    let mut render = Render::default();
    // PR closed: SSE without a check run.
    let mut sse_only = false;

    // A batch of GrantObserved/GrantCleared with no outcome event is the settled
    // awaiting-approval case, projected after the loop from the folded state.
    let mut observe_batch = false;
    let mut gate_outcome = false;

    for e in evs {
        match e {
            Event::GatePassed(_) => render.raise(2, true, "success"),
            Event::Classified(_) => render.raise(1, false, ""),
            Event::GateTargetRequested(ev) => {
                gate_outcome = true;
                actions.push(
                    RequestGrant {
                        class: ev.class.clone(),
                        target: ev.target.clone(),
                        requester: ev.requester.clone(),
                    }
                    .into(),
                );
                render.raise(1, false, "");
            }
            Event::GrantObserved(_) | Event::GrantCleared(_) => observe_batch = true,
            Event::GateSatisfied(_) => {
                gate_outcome = true;
                render.raise(2, true, "success");
            }
            Event::GateBlocked(ev) => {
                // Slot collisions keep waiting (non-terminal); denied, revoked and
                // expired are a terminal action_required.
                gate_outcome = true;
                if matches!(ev.reason, BlockReason::SlotSelf | BlockReason::SlotForeign) {
                    render.raise(1, false, "");
                } else {
                    render.raise(2, true, "action_required");
                }
            }
            Event::RunQueued(ev) => {
                actions.push(
                    StartRun {
                        kind: ev.kind.clone(),
                        sha: ev.sha.clone(),
                        branch: ev.branch.clone(),
                        execution_id: ev.execution_id.clone(),
                    }
                    .into(),
                );
                render.raise(1, false, "");
            }
            Event::RunStarted(_) => render.raise(1, false, ""),
            Event::RunStartFailed(_) => render.raise(3, true, "failure"),
            Event::RunAdopted(ev) => {
                actions.push(
                    AdoptRun {
                        kind: ev.kind.clone(),
                        execution_id: ev.execution_id.clone(),
                        sha: ev.sha.clone(),
                        build_ref: ev.build_ref.clone(),
                    }
                    .into(),
                );
                render.raise(1, false, "");
            }
            Event::RunSuperseded(ev) => actions.push(
                CancelRun {
                    kind: ev.kind.clone(),
                    old_execution_id: ev.old_execution_id.clone(),
                    old_build_ref: ev.old_build_ref.clone(),
                    new_execution_id: ev.new_execution_id.clone(),
                }
                .into(),
            ),
            Event::ClaimReleased(ev) => actions.push(
                ReleaseClaim {
                    pr: ev.pr,
                    environment: ev.environment.clone(),
                }
                .into(),
            ),
            Event::TargetRevoked(ev) => actions.push(
                RevokeGrant {
                    class: ev.class.clone(),
                    target: ev.target.clone(),
                    pr: ev.pr,
                    environment: ev.env.clone(),
                }
                .into(),
            ),
            Event::PRClosedRecorded(_) => sse_only = true,
            Event::GateReleased(_)
            | Event::RunCompleted(_)
            | Event::AdminGrantReleased(_)
            | Event::AdminExecutionCancelled(_)
            | Event::AdminGateSatisfied(_)
            | Event::AdminCheckOverridden(_) => {}
        }
    }

    // Waiting on a human is not a verdict: an observe batch that left the gate
    // Pending renders in progress.
    if observe_batch && !gate_outcome && matches!(state.gate, GateState::Pending(_)) {
        render.raise(1, false, "");
    }

    if let Some(o) = &state.check_override {
        render = Render {
            precedence: 2,
            action: RenderCheckRun {
                terminal: true,
                conclusion: o.conclusion.clone(),
            },
        };
    }

    if sse_only {
        actions.push(PublishSSE {}.into());
    } else if render.precedence > 0 {
        actions.push(render.action.into());
        actions.push(PublishSSE {}.into());
    }
    actions
}

/// The winning render so far.
#[derive(Default)]
struct Render {
    precedence: u8,
    action: RenderCheckRun,
}

impl Render {
    /// Takes this render if it outranks the current one.
    fn raise(&mut self, precedence: u8, terminal: bool, conclusion: &str) {
        if self.precedence < precedence {
            self.precedence = precedence;
            self.action = RenderCheckRun {
                terminal,
                conclusion: conclusion.to_string(),
            };
        }
    }
}

#[cfg(test)]
pub(super) mod tests;
