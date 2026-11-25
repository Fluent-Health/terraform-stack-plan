use std::collections::BTreeMap;

use super::*;

#[test]
fn version_present() {
    const { assert!(VERSION >= 1) };
}

#[test]
fn init_json_field_names() {
    let init = Init {
        id: "exec-1".into(),
        repo: "owner/repo".into(),
        sha: "abc123".into(),
        pr: 42,
        environment: "staging".into(),
        log_url: "https://ci/log".into(),
        context: "iam/staging".into(),
        stacks: vec![StackState {
            path: "stacks/a".into(),
            project: "proj-a".into(),
            status: Status::Pending,
            ..StackState::default()
        }],
        edges: vec![Edge {
            from: "stacks/a".into(),
            to: "stacks/b".into(),
        }],
    };
    let got = serde_json::to_string(&init).unwrap();
    for want in [
        r#""environment":"staging""#,
        r#""pr":42"#,
        r#""log_url":"https://ci/log""#,
        r#""path":"stacks/a""#,
        r#""project":"proj-a""#,
        r#""status":"pending""#,
        r#""from":"stacks/a""#,
        r#""to":"stacks/b""#,
    ] {
        assert!(got.contains(want), "Init JSON missing {want}\n got: {got}");
    }
    assert!(!got.contains(r#""tier""#), "legacy tier key in {got}");
}

#[test]
fn finalize_gates_round_trip() {
    let f = Finalize {
        id: "exec-1".into(),
        report_markdown: "# report".into(),
        projects: BTreeMap::from([("stacks/a".into(), "proj-a".into())]),
        gates: vec![GateTarget {
            class: "iam".into(),
            target: "proj-a".into(),
        }],
        moving: vec!["stacks/c".into()],
        failed: false,
        ..Finalize::default()
    };
    let got: Finalize = serde_json::from_str(&serde_json::to_string(&f).unwrap()).unwrap();
    assert_eq!(got.gates, f.gates);
    assert_eq!(got.projects["stacks/a"], "proj-a");
}

#[test]
fn log_chunk_round_trip() {
    let c = LogChunk {
        id: "e1".into(),
        stack: "stacks/a".into(),
        data: "hello\n".into(),
    };
    let b = serde_json::to_string(&c).unwrap();
    let got: LogChunk = serde_json::from_str(&b).unwrap();
    assert_eq!(got, c);
    assert!(b.contains(r#""stack":"stacks/a""#));
    assert!(b.contains(r#""data":"hello\n""#));
}

#[test]
fn counts_omitted_when_nil() {
    let b = serde_json::to_string(&StackState {
        path: "a".into(),
        ..StackState::default()
    })
    .unwrap();
    assert_eq!(b, r#"{"path":"a"}"#);
}

#[test]
fn counts_round_trip() {
    let state = StackState {
        path: "a".into(),
        counts: Some(Counts {
            add: 6,
            change: 2,
            replace: 1,
            moved: 1,
            ..Counts::default()
        }),
        ..StackState::default()
    };
    let out: StackState = serde_json::from_str(&serde_json::to_string(&state).unwrap()).unwrap();
    assert_eq!(out.counts, state.counts);
}

#[test]
fn finalize_counts_map() {
    let f = Finalize {
        id: "e1".into(),
        counts: BTreeMap::from([(
            "a".into(),
            Counts {
                destroy: 2,
                ..Counts::default()
            },
        )]),
        ..Finalize::default()
    };
    let out: Finalize = serde_json::from_str(&serde_json::to_string(&f).unwrap()).unwrap();
    assert_eq!(out.counts["a"].destroy, 2);
}

#[test]
fn status_valid_and_parse() {
    assert_eq!(Status::parse("pending"), Ok(Status::Pending));
    assert!(Status::parse("invalid").is_err());
    assert_eq!(Status::parse(""), Ok(Status::Unset));
    assert_eq!(Status::parse("gated"), Ok(Status::Gated));
    assert!(Status::parse("bogus").is_err());
    let status: Status = serde_json::from_str(r#""running""#).unwrap();
    assert_eq!(status, Status::Running);
}

#[test]
fn phase_ticking_and_valid() {
    assert!(Phase::new(Phase::PLANNING).ticking());
    assert!(!Phase::new(Phase::WARMING).ticking());
    assert!(Phase::new(Phase::VERIFYING).valid());
    assert!(!Phase::default().valid());
}

// status_test.go

#[test]
fn all_known_statuses_are_valid() {
    for s in Status::ALL {
        assert_eq!(Status::parse(s.as_str()), Ok(s));
    }
    assert_eq!(Status::parse(""), Ok(Status::Unset));
    assert!(Status::parse("bogus").is_err());
}

#[test]
fn parse_status_rejects_unknown_with_code() {
    assert!(Status::parse("planned").is_ok());
    let err = Status::parse("bogus").unwrap_err();
    assert!(err.to_string().starts_with("WIRE-001: "), "{err}");
}

#[test]
fn status_unmarshal_json() {
    let u: Update = serde_json::from_str(r#"{"id":"x","stack":"a","status":"planned"}"#).unwrap();
    assert_eq!(u.status, Status::Planned);
    assert!(serde_json::from_str::<Update>(r#"{"id":"x","stack":"a","status":"bogus"}"#).is_err());
    let u2: Update = serde_json::from_str(r#"{"id":"x","stack":"a"}"#).unwrap();
    assert_eq!(u2.status, Status::Unset);
}

// codec_alias_test.go: Counts and Category are the domain types (re-exported, so
// the same types), and their wire shape is unchanged.
#[test]
fn events_types_are_domain_aliases() {
    let c: Counts = Counts {
        add: 2,
        change: 1,
        ..Counts::default()
    };
    assert_eq!(
        serde_json::to_string(&c).unwrap(),
        r#"{"add":2,"change":1}"#
    );
    let cat: Category = Category {
        name: "iam".into(),
        icon: "🔐".into(),
        ..Category::default()
    };
    assert_eq!(
        serde_json::to_string(&cat).unwrap(),
        r#"{"name":"iam","icon":"🔐"}"#
    );
}
