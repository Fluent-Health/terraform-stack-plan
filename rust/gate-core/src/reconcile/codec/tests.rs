// Ported case by case from internal/reconcile/codec_test.go, plus fixtures of the
// JSON Go writes.
use crate::approval::GrantState;
use crate::reconcile::decide::tests::{gt, pending, satisfied, tgt};
use crate::reconcile::evolve::tests::corpus;
use crate::reconcile::*;

#[test]
fn event_codec_round_trips_every_variant() {
    for e in corpus() {
        let (tag, data) = marshal_event(&e).unwrap();
        let got = unmarshal_event(tag, &data).unwrap();
        assert_eq!(got, e, "tag {tag}");
    }
}

#[test]
fn unmarshal_event_unknown_tag_errors() {
    assert!(unmarshal_event("NoSuchEvent", b"{}").is_err());
}

#[test]
fn snapshot_round_trips_every_gate_variant() {
    let gates = [
        GateState::NotClassified,
        GateState::Clean,
        pending(vec![tgt("c", "t", "g1", GrantState::Awaiting)], "sa"),
        satisfied(vec![tgt("c", "t", "g1", GrantState::Active)], "sa"),
        GateState::Blocked(Blocked {
            targets: vec![tgt("c", "t", "", GrantState::Revoked)],
            lease: Lease {
                requester: "sa".into(),
            },
            by: Blocker {
                reason: BlockReason::SlotForeign,
                by_pr: 9,
                by_env: "prod".into(),
            },
        }),
    ];
    for g in gates {
        let cs = ChangeSet {
            pr: 7,
            environment: "nonprod".into(),
            gate: g,
            ..ChangeSet::default()
        };
        let got = unmarshal_snapshot(&marshal_snapshot(&cs).unwrap()).unwrap();
        assert_eq!(got, cs);
    }
}

#[test]
fn replay_determinism_and_snapshot_equals_fold() {
    let evs: Vec<Event> = vec![
        Classified {
            gates: vec![gt("iam", "proj-a")],
        }
        .into(),
        GateTargetRequested {
            class: "iam".into(),
            target: "proj-a".into(),
            requester: "sa".into(),
        }
        .into(),
        GrantObserved {
            class: "iam".into(),
            target: "proj-a".into(),
            name: "g1".into(),
            state: GrantState::Active,
            requester: "sa".into(),
        }
        .into(),
        GateSatisfied {}.into(),
    ];
    let fold = || {
        evs.iter().fold(
            ChangeSet {
                pr: 7,
                environment: "nonprod".into(),
                ..ChangeSet::default()
            },
            evolve,
        )
    };
    let (a, b) = (fold(), fold());
    assert_eq!(a, b);
    let reloaded = unmarshal_snapshot(&marshal_snapshot(&a).unwrap()).unwrap();
    assert_eq!(reloaded, a);
}

// --- Go's JSON, as encoding/json writes it ---

#[test]
fn reads_go_event_json() {
    let e = unmarshal_event(
        "RunQueued",
        br#"{"Kind":"plan","SHA":"abc","Branch":"b","ExecutionID":"run-1","Attempt":2}"#,
    )
    .unwrap();
    assert_eq!(
        e,
        RunQueued {
            kind: "plan".into(),
            sha: "abc".into(),
            branch: "b".into(),
            execution_id: "run-1".into(),
            attempt: 2,
        }
        .into()
    );
    let e = unmarshal_event(
        "GateBlocked",
        br#"{"Reason":"slot_foreign_open","ByPR":9,"ByEnv":"prod"}"#,
    )
    .unwrap();
    assert_eq!(
        e,
        GateBlocked {
            reason: BlockReason::SlotForeign,
            by_pr: 9,
            by_env: "prod".into()
        }
        .into()
    );
    // A nil slice is `null`.
    let e = unmarshal_event("Classified", br#"{"Gates":null}"#).unwrap();
    assert_eq!(e, Classified { gates: vec![] }.into());
}

#[test]
fn writes_go_event_json() {
    let (tag, data) = marshal_event(
        &TargetRevoked {
            class: "iam".into(),
            target: "p".into(),
            pr: 7,
            env: "prod".into(),
        }
        .into(),
    )
    .unwrap();
    assert_eq!(tag, "TargetRevoked");
    assert_eq!(
        String::from_utf8(data).unwrap(),
        r#"{"Class":"iam","Target":"p","PR":7,"Env":"prod"}"#
    );
    let (_, data) = marshal_event(&GateSatisfied {}.into()).unwrap();
    assert_eq!(data, b"{}");
}

#[test]
fn reads_go_snapshot_json() {
    let go = br#"{"pr":7,"environment":"nonprod","gate_kind":"Pending","gate":{"Targets":[{"Class":"iam","Target":"p","GrantName":"","Grant":""}],"Lease":{"Requester":""}},"runs":{"plan":{"ExecutionID":"run-7","Kind":"plan","SHA":"abc","Branch":"b","Attempt":1,"BuildRef":"","Phase":"start_failed"}}}"#;
    let cs = unmarshal_snapshot(go).unwrap();
    assert_eq!(
        cs.gate,
        pending(vec![tgt("iam", "p", "", GrantState::Absent)], "")
    );
    assert_eq!(cs.runs[RUN_KIND_PLAN].phase, RunPhase::StartFailed);

    let go = br#"{"pr":7,"environment":"nonprod","gate_kind":"Satisfied","gate":{"Targets":null,"Lease":{"Requester":"sa"}}}"#;
    assert_eq!(
        unmarshal_snapshot(go).unwrap().gate,
        satisfied(vec![], "sa")
    );

    let go = br#"{"pr":7,"environment":"nonprod","gate_kind":"NotClassified","gate":{}}"#;
    assert_eq!(
        unmarshal_snapshot(go).unwrap().gate,
        GateState::NotClassified
    );

    assert!(unmarshal_snapshot(br#"{"gate_kind":"Nope","gate":{}}"#).is_err());
}

#[test]
fn writes_go_snapshot_json() {
    let cs = ChangeSet {
        pr: 7,
        environment: "nonprod".into(),
        gate: GateState::Clean,
        ..ChangeSet::default()
    };
    assert_eq!(
        String::from_utf8(marshal_snapshot(&cs).unwrap()).unwrap(),
        r#"{"pr":7,"environment":"nonprod","gate_kind":"Clean","gate":{}}"#
    );
}
