// Ported case by case from internal/claims/codec_test.go, plus Go JSON fixtures.
use crate::claims::decide::tests::{acquired, leases, t0};
use crate::claims::*;

#[test]
fn claim_codec_round_trip() {
    for e in [
        acquired(7, &["a"], t0()),
        ClaimRenewed {
            pr: 7,
            expires_at: t0(),
        }
        .into(),
        ClaimReleased { pr: 7 }.into(),
        ClaimStackReleased {
            pr: 7,
            stack: "a".into(),
        }
        .into(),
    ] {
        let (tag, data) = marshal_event(&e).unwrap();
        assert_eq!(unmarshal_event(tag, &data).unwrap(), e, "tag {tag}");
    }
}

#[test]
fn claim_snapshot_round_trip() {
    let cs = evolve(ClaimSet::new(), &acquired(7, &["a", "b"], t0()));
    assert_eq!(
        unmarshal_snapshot(&marshal_snapshot(&cs).unwrap()).unwrap(),
        cs
    );
}

#[test]
fn claim_codec_unknown_tag() {
    assert!(unmarshal_event("NoSuchEvent", b"{}").is_err());
}

#[test]
fn claim_codec_replay_determinism() {
    let events: Vec<Event> = vec![
        acquired(7, &["a", "b"], leases(1)),
        ClaimRenewed {
            pr: 7,
            expires_at: leases(2),
        }
        .into(),
        ClaimStackReleased {
            pr: 7,
            stack: "b".into(),
        }
        .into(),
    ];
    let fold = || events.iter().fold(ClaimSet::new(), evolve);
    assert_eq!(fold(), fold());
}

#[test]
fn claim_snapshot_equals_direct_fold() {
    let s = evolve(ClaimSet::new(), &acquired(7, &["x"], leases(1)));
    let s = evolve(
        s,
        &ClaimRenewed {
            pr: 7,
            expires_at: leases(2),
        }
        .into(),
    );
    assert_eq!(
        unmarshal_snapshot(&marshal_snapshot(&s).unwrap()).unwrap(),
        s
    );
}

#[test]
fn reads_go_json() {
    // Go writes time.Time as RFC 3339 with nanoseconds and the local offset.
    let e = unmarshal_event(
        "ClaimAcquired",
        br#"{"PR":7,"Stacks":["a"],"ExpiresAt":"2026-06-24T17:30:00.123456789+05:00"}"#,
    )
    .unwrap();
    let Event::ClaimAcquired(e) = e else {
        panic!("{e:?}")
    };
    assert_eq!(
        e.expires_at,
        "2026-06-24T12:30:00.123456789Z".parse().unwrap()
    );
    let cs = unmarshal_snapshot(br#"{"a":{"PR":7,"ExpiresAt":"2026-06-24T12:30:00Z"}}"#).unwrap();
    assert_eq!(cs["a"].pr, 7);
    assert!(unmarshal_snapshot(b"null").unwrap().is_empty());
}
