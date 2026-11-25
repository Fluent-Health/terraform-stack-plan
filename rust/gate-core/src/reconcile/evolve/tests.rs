// Ported case by case from internal/reconcile/evolve_test.go.
use crate::approval::GrantState;
use crate::reconcile::decide::tests::{gt, pending, satisfied, tgt};
use crate::reconcile::*;

#[test]
fn evolve_gate_satisfied() {
    let prior = ChangeSet {
        gate: pending(vec![tgt("c", "t", "", GrantState::Active)], ""),
        ..ChangeSet::default()
    };
    let got = evolve(prior, &GateSatisfied {}.into());
    assert!(
        matches!(got.gate, GateState::Satisfied(_)),
        "{:?}",
        got.gate
    );
}

#[test]
fn evolve_admin_events() {
    let prior = ChangeSet {
        pr: 7,
        environment: "staging".into(),
        gate: pending(
            vec![
                tgt("iam", "proj-a", "", GrantState::Awaiting),
                tgt("iam", "proj-b", "", GrantState::Awaiting),
            ],
            "",
        ),
        ..ChangeSet::default()
    };

    let got = evolve(
        prior.clone(),
        &AdminGrantReleased {
            pr: 7,
            environment: "staging".into(),
            class: "iam".into(),
            target: "proj-a".into(),
            ..AdminGrantReleased::default()
        }
        .into(),
    );
    assert_eq!(got.gate.targets().unwrap()[0].grant, GrantState::Revoked);

    let got2 = evolve(
        prior.clone(),
        &AdminGateSatisfied {
            pr: 7,
            environment: "staging".into(),
            class: "iam".into(),
            target: "proj-a".into(),
            ..AdminGateSatisfied::default()
        }
        .into(),
    );
    assert!(matches!(got2.gate, GateState::Pending(_)));
    assert_eq!(got2.gate.targets().unwrap()[0].grant, GrantState::Active);

    let got3 = evolve(
        prior,
        &AdminCheckOverridden {
            pr: 7,
            env: "staging".into(),
            check_name: "check-1".into(),
            conclusion: "success".into(),
            ..AdminCheckOverridden::default()
        }
        .into(),
    );
    let o = got3.check_override.expect("check override");
    assert_eq!(o.check_name, "check-1");
    assert_eq!(o.conclusion, "success");
}

#[test]
fn evolve_gate_released_and_passed_both_clean() {
    for e in [Event::from(GatePassed {}), GateReleased {}.into()] {
        let got = evolve(
            ChangeSet {
                gate: GateState::Pending(Pending::default()),
                ..ChangeSet::default()
            },
            &e,
        );
        assert_eq!(got.gate, GateState::Clean, "{e:?}");
    }
}

/// One example of every event variant, for the totality and determinism
/// properties. Extend it with every new event.
pub(crate) fn corpus() -> Vec<Event> {
    vec![
        Classified {
            gates: vec![gt("c", "t")],
        }
        .into(),
        GrantObserved {
            class: "c".into(),
            target: "t".into(),
            name: "g1".into(),
            state: GrantState::Active,
            requester: "sa".into(),
        }
        .into(),
        GrantCleared {
            class: "c".into(),
            target: "t".into(),
        }
        .into(),
        GateTargetRequested {
            class: "c".into(),
            target: "t".into(),
            requester: String::new(),
        }
        .into(),
        GateSatisfied {}.into(),
        GateBlocked::new(BlockReason::Denied).into(),
        TargetRevoked {
            class: "c".into(),
            target: "t".into(),
            ..TargetRevoked::default()
        }
        .into(),
        GatePassed {}.into(),
        GateReleased {}.into(),
        ClaimReleased {
            pr: 1,
            environment: "nonprod".into(),
        }
        .into(),
        PRClosedRecorded {}.into(),
        RunQueued {
            kind: RUN_KIND_PLAN.into(),
            sha: "abc123".into(),
            branch: "feat/x".into(),
            execution_id: "run-1-nonprod-plan-abc123-a1".into(),
            attempt: 1,
        }
        .into(),
        RunStarted {
            kind: RUN_KIND_PLAN.into(),
            execution_id: "run-1-nonprod-plan-abc123-a1".into(),
            build_ref: "b-1".into(),
        }
        .into(),
        RunStartFailed {
            kind: RUN_KIND_PLAN.into(),
            execution_id: "run-1-nonprod-plan-abc123-a1".into(),
            reason: "api error".into(),
        }
        .into(),
        RunSuperseded {
            kind: RUN_KIND_PLAN.into(),
            old_execution_id: "run-1-nonprod-plan-abc123-a1".into(),
            old_build_ref: "b-1".into(),
            new_execution_id: "run-1-nonprod-plan-def456-a1".into(),
            new_sha: "def456".into(),
        }
        .into(),
        RunAdopted {
            kind: RUN_KIND_PLAN.into(),
            execution_id: "run-7-nonprod-plan-abc-a2".into(),
            sha: "abc".into(),
            branch: "feat/x".into(),
            attempt: 2,
            build_ref: "build-new".into(),
        }
        .into(),
        RunCompleted {
            kind: RUN_KIND_PLAN.into(),
            execution_id: "run-1-nonprod-plan-abc123-a1".into(),
        }
        .into(),
        AdminGrantReleased {
            pr: 7,
            environment: "staging".into(),
            class: "iam".into(),
            target: "proj-a".into(),
            actor: "admin".into(),
            reason: "override".into(),
        }
        .into(),
        AdminExecutionCancelled {
            kind: "plan".into(),
            execution_id: "exec-1".into(),
            actor: "admin".into(),
            reason: "stuck".into(),
        }
        .into(),
        AdminGateSatisfied {
            pr: 7,
            environment: "staging".into(),
            class: "iam".into(),
            target: "proj-a".into(),
            actor: "admin".into(),
            reason: "by phone".into(),
        }
        .into(),
        AdminCheckOverridden {
            pr: 7,
            env: "staging".into(),
            check_name: "terraform/staging".into(),
            conclusion: "success".into(),
            actor: "admin".into(),
            reason: "bypass".into(),
        }
        .into(),
    ]
}

fn prior_states() -> Vec<ChangeSet> {
    let with = |gate| ChangeSet {
        gate,
        ..ChangeSet::default()
    };
    vec![
        ChangeSet::default(),
        with(GateState::NotClassified),
        with(GateState::Clean),
        with(pending(vec![tgt("c", "t", "g1", GrantState::Active)], "sa")),
        with(satisfied(vec![tgt("c", "t", "", GrantState::Active)], "")),
        with(GateState::Blocked(Blocked {
            targets: vec![tgt("c", "t", "", GrantState::Absent)],
            lease: Lease::default(),
            by: Blocker::new(BlockReason::Denied),
        })),
    ]
}

// Go recovers a panic per (state, event) pair; a Rust panic fails the test the same way.
#[test]
fn evolve_is_total() {
    for st in prior_states() {
        for e in corpus() {
            let _ = evolve(st.clone(), &e);
        }
    }
}

#[test]
fn evolve_is_deterministic() {
    for st in prior_states() {
        let fold = || corpus().iter().fold(st.clone(), evolve);
        assert_eq!(fold(), fold(), "prior {:?}", st.gate);
    }
}
