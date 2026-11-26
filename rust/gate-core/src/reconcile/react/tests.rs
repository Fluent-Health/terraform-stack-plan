// Ported case by case from internal/reconcile/react_test.go.
use crate::approval::GrantState;
use crate::reconcile::decide::tests::{gt, pending, satisfied, tgt};
use crate::reconcile::*;

fn with_gate(gate: GateState) -> ChangeSet {
    ChangeSet {
        gate,
        ..ChangeSet::default()
    }
}

fn blocked(reason: BlockReason) -> GateState {
    GateState::Blocked(Blocked {
        targets: vec![],
        lease: Lease::default(),
        by: Blocker::new(reason),
    })
}

pub(crate) fn render(terminal: bool, conclusion: &str) -> Action {
    RenderCheckRun {
        terminal,
        conclusion: conclusion.into(),
    }
    .into()
}

pub(crate) fn sse() -> Action {
    PublishSSE {}.into()
}

fn request(class: &str, target: &str) -> Action {
    RequestGrant {
        class: class.into(),
        target: target.into(),
        requester: String::new(),
    }
    .into()
}

fn requested(class: &str, target: &str) -> Event {
    GateTargetRequested {
        class: class.into(),
        target: target.into(),
        requester: String::new(),
    }
    .into()
}

fn revoked(pr: i64, env: &str) -> Event {
    TargetRevoked {
        class: "c".into(),
        target: "t".into(),
        pr,
        env: env.into(),
    }
    .into()
}

fn revoke(pr: i64, env: &str) -> Action {
    RevokeGrant {
        class: "c".into(),
        target: "t".into(),
        pr,
        environment: env.into(),
    }
    .into()
}

fn observed(state: GrantState) -> Event {
    GrantObserved {
        class: "c".into(),
        target: "t".into(),
        name: "g1".into(),
        state,
        requester: String::new(),
    }
    .into()
}

// --- RunnerFinalize ---

#[test]
fn react_gate_passed_renders_success() {
    let got = react(&with_gate(GateState::Clean), &[GatePassed {}.into()]);
    assert_eq!(got, vec![render(true, "success"), sse()]);
}

#[test]
fn react_gated_finalize_requests_and_renders() {
    let evs = [
        Classified {
            gates: vec![gt("c", "t")],
        }
        .into(),
        requested("c", "t"),
    ];
    let got = react(&with_gate(GateState::Pending(Pending::default())), &evs);
    assert_eq!(got, vec![request("c", "t"), render(false, ""), sse()]);
}

#[test]
fn react_failure_precedence_over_in_progress() {
    // Failure wins the render, and the RequestGrant still fires: actions accumulate
    // independently of render precedence.
    let evs = [
        RunStartFailed {
            kind: RUN_KIND_PLAN.into(),
            execution_id: "e1".into(),
            reason: "boom".into(),
        }
        .into(),
        requested("c", "t"),
    ];
    let got = react(&ChangeSet::default(), &evs);
    let renders: Vec<&Action> = got
        .iter()
        .filter(|a| matches!(a, Action::RenderCheckRun(_)))
        .collect();
    assert_eq!(renders, vec![&render(true, "failure")], "{got:?}");
    assert!(got.contains(&request("c", "t")), "{got:?}");
}

#[test]
fn react_apply_cleanup_no_presentation() {
    let evs = [
        ClaimReleased {
            pr: 7,
            environment: "nonprod".into(),
        }
        .into(),
        revoked(7, "nonprod"),
        GateReleased {}.into(),
    ];
    let got = react(&ChangeSet::default(), &evs);
    let want: Vec<Action> = vec![
        ReleaseClaim {
            pr: 7,
            environment: "nonprod".into(),
        }
        .into(),
        revoke(7, "nonprod"),
    ];
    assert_eq!(got, want);
}

// --- observe / collision ---

#[test]
fn react_observe_satisfied_renders_success() {
    let evs = [observed(GrantState::Active), GateSatisfied {}.into()];
    let got = react(&with_gate(satisfied(vec![], "")), &evs);
    assert_eq!(got, vec![render(true, "success"), sse()]);
}

#[test]
fn react_observe_terminal_blocked_renders_action_required() {
    for r in [
        BlockReason::Denied,
        BlockReason::Revoked,
        BlockReason::Expired,
    ] {
        let evs = [observed(GrantState::Denied), GateBlocked::new(r).into()];
        let got = react(&with_gate(blocked(r)), &evs);
        assert_eq!(got, vec![render(true, "action_required"), sse()], "{r:?}");
    }
}

#[test]
fn react_collision_slot_blocked_renders_non_terminal() {
    for r in [BlockReason::SlotSelf, BlockReason::SlotForeign] {
        let evs = [GateBlocked {
            reason: r,
            by_pr: 7,
            by_env: "prod".into(),
        }
        .into()];
        let got = react(&with_gate(blocked(r)), &evs);
        assert_eq!(got, vec![render(false, ""), sse()], "{r:?}");
    }
}

#[test]
fn react_collision_abandoned_revokes_then_requests_then_renders() {
    let evs = [revoked(7, "staging"), requested("c", "t")];
    let got = react(&with_gate(GateState::Pending(Pending::default())), &evs);
    assert_eq!(
        got,
        vec![
            revoke(7, "staging"),
            request("c", "t"),
            render(false, ""),
            sse()
        ]
    );
}

#[test]
fn react_observe_settled_pending_renders_non_terminal() {
    // Awaiting a human keeps the check in progress, never action_required.
    let evs = [observed(GrantState::Awaiting)];
    let got = react(
        &with_gate(pending(vec![tgt("c", "t", "g1", GrantState::Awaiting)], "")),
        &evs,
    );
    assert_eq!(got, vec![render(false, ""), sse()]);
}

#[test]
fn react_pr_closed_sse_only() {
    let evs = [
        revoked(7, "nonprod"),
        PRClosedRecorded {}.into(),
        GateBlocked::new(BlockReason::Revoked).into(),
    ];
    let got = react(&with_gate(blocked(BlockReason::Revoked)), &evs);
    assert_eq!(got, vec![revoke(7, "nonprod"), sse()]);
}

#[test]
fn react_check_override() {
    let state = ChangeSet {
        pr: 7,
        environment: "staging".into(),
        check_override: Some(CheckOverride {
            check_name: "check-1".into(),
            conclusion: "success".into(),
            ..CheckOverride::default()
        }),
        ..ChangeSet::default()
    };
    let got = react(&state, &[requested("c", "t")]);
    assert_eq!(got, vec![request("c", "t"), render(true, "success"), sse()]);
}
