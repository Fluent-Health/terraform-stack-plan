// Ported case by case from internal/execution/codec_test.go, plus Go JSON fixtures.
use std::collections::BTreeMap;

use crate::events::{Category, Counts, Phase, Status};
use crate::execution::*;

fn all_event_variants() -> Vec<Event> {
    vec![
        Started {
            exec: State {
                id: "e1".into(),
                repo: "r".into(),
                sha: "abc".into(),
                stacks: vec![Stack {
                    path: "a".into(),
                    run_status: Status::Running,
                    ..Stack::default()
                }],
                ..State::default()
            },
        }
        .into(),
        PhaseChanged {
            phase: Phase::new(Phase::APPLYING),
            label: "applying".into(),
            pct: Some(50),
            id: "e1".into(),
            pr: 7,
            environment: "nonprod".into(),
            context: "terraform/nonprod".into(),
            repo: "r".into(),
            sha: "abc".into(),
            log_url: String::new(),
        }
        .into(),
        StackStatusChanged {
            stack: "a".into(),
            status: Status::Failed,
            detail: "boom".into(),
        }
        .into(),
        Failed {}.into(),
        Succeeded {}.into(),
        StacksAnnotated {
            projects: BTreeMap::from([("a".into(), "p".into())]),
            categories: BTreeMap::from([(
                "a".into(),
                vec![Category {
                    name: "iam".into(),
                    ..Category::default()
                }],
            )]),
            counts: BTreeMap::from([("a".into(), Counts::default())]),
            moving: vec!["b".into()],
        }
        .into(),
        Superseded {
            by: "new-exec".into(),
        }
        .into(),
    ]
}

#[test]
fn codec_round_trips_every_variant() {
    for ev in all_event_variants() {
        let (tag, data) = marshal_event(&ev).unwrap();
        assert_eq!(unmarshal_event(tag, &data).unwrap(), ev, "tag {tag}");
    }
}

#[test]
fn codec_unknown_tag_errors() {
    assert!(unmarshal_event("Nope", b"{}").is_err());
}

#[test]
fn snapshot_round_trip() {
    let s = State {
        id: "e1".into(),
        phase: Phase::new(Phase::APPLYING),
        stacks: vec![Stack {
            path: "a".into(),
            run_status: Status::Planned,
            ..Stack::default()
        }],
        ..State::default()
    };
    assert_eq!(
        unmarshal_snapshot(&marshal_snapshot(&s).unwrap()).unwrap(),
        s
    );
}

#[test]
fn reads_go_snapshot_json() {
    let go = br#"{"ID":"e1","Repo":"r","SHA":"abc","LogURL":"","PR":7,"Environment":"nonprod","Context":"plan/nonprod","Phase":"planning","ProgressLabel":"","ProgressPct":null,"Status":"in_progress","SupersededBy":"","Stacks":[{"Path":"a","Project":"p","RunStatus":"planned","Detail":"","Categories":null,"Counts":{"add":1}}],"Edges":null}"#;
    let s = unmarshal_snapshot(go).unwrap();
    assert_eq!(s.pr, 7);
    assert_eq!(s.stacks[0].run_status, Status::Planned);
    assert_eq!(s.stacks[0].counts.map(|c| c.add), Some(1));
    assert!(s.edges.is_empty());
    // An unknown stack status fails the decode, as Go's Status.UnmarshalJSON does.
    assert!(
        unmarshal_event(
            "StackStatusChanged",
            br#"{"Stack":"a","Status":"bogus","Detail":""}"#
        )
        .is_err()
    );
}
