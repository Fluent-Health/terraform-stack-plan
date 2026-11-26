// action_test.go: Go checks each variant implements the Action interface.
use super::*;

#[test]
fn action_variants() {
    let actions: Vec<Action> = vec![
        RequestGrant {
            class: "iam".into(),
            target: "p".into(),
            ..RequestGrant::default()
        }
        .into(),
        RevokeGrant {
            class: "iam".into(),
            target: "p".into(),
            pr: 7,
            environment: "staging".into(),
        }
        .into(),
        RenderCheckRun {
            terminal: true,
            conclusion: "success".into(),
        }
        .into(),
        PostCommitStatus {
            state: "success".into(),
            ..PostCommitStatus::default()
        }
        .into(),
        PublishSSE {}.into(),
        ReleaseClaim {
            pr: 7,
            environment: "staging".into(),
        }
        .into(),
    ];
    assert_eq!(actions.len(), 6);
}
