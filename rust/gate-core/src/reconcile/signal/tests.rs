// signal_test.go: Go checks each variant implements the Signal interface.
use super::*;

#[test]
fn signal_variants() {
    let sigs: Vec<Signal> = vec![
        RunnerFinalize {
            gates: vec![GateTarget {
                class: "iam".into(),
                target: "p".into(),
            }],
            apply_context: false,
        }
        .into(),
        PRClosed {}.into(),
        GrantsObserved {
            grants: vec![ObservedGrant {
                class: "iam".into(),
                target: "p".into(),
                state: GrantState::Active,
                ..ObservedGrant::default()
            }],
        }
        .into(),
        GateTick { grants: vec![] }.into(),
        ApplySucceeded {}.into(),
    ];
    assert_eq!(sigs.len(), 5);
}
