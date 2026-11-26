// state_test.go: Go checks that each variant implements the GateState interface;
// here the enum makes that structural, so the tests build each variant.
use super::*;

#[test]
fn gate_state_variants() {
    let lease = Lease {
        requester: "sa0".into(),
    };
    let gs = [
        GateState::NotClassified,
        GateState::Clean,
        GateState::Pending(Pending {
            lease: lease.clone(),
            ..Pending::default()
        }),
        GateState::Satisfied(Satisfied {
            lease,
            ..Satisfied::default()
        }),
        GateState::Blocked(Blocked {
            targets: vec![],
            lease: Lease::default(),
            by: Blocker::new(BlockReason::Denied),
        }),
    ];
    assert_eq!(gs.len(), 5);
}

#[test]
fn target_carries_observed_grant_state() {
    let tg = Target {
        class: "iam".into(),
        target: "proj-a".into(),
        grant_name: "g1".into(),
        grant: GrantState::Active,
    };
    assert_eq!(tg.grant, GrantState::Active);
}
