use super::*;

#[test]
fn grant_state_open() {
    for s in [
        GrantState::Awaiting,
        GrantState::Activating,
        GrantState::Active,
    ] {
        assert!(s.open(), "{s:?} should be open");
    }
    for s in [
        GrantState::Denied,
        GrantState::Revoked,
        GrantState::Expired,
        GrantState::Absent,
        GrantState::Other("ACTIVATION_FAILED".into()),
    ] {
        assert!(!s.open(), "{s:?} should be closed");
    }
}

#[test]
fn an_unknown_state_round_trips() {
    let s: GrantState = serde_json::from_str(r#""WITHDRAWN""#).unwrap();
    assert_eq!(s, GrantState::Other("WITHDRAWN".into()));
    assert_eq!(serde_json::to_string(&s).unwrap(), r#""WITHDRAWN""#);
    let known: GrantState = serde_json::from_str(r#""ACTIVE""#).unwrap();
    assert_eq!(known, GrantState::Active);
    assert_eq!(
        serde_json::from_str::<GrantState>(r#""""#).unwrap(),
        GrantState::Absent
    );
}

#[test]
fn slot_collision_error_message() {
    let e = SlotCollisionError {
        blocking_grant: Grant {
            request: Request {
                pr: 7,
                environment: "nonprod".into(),
                ..Request::default()
            },
            ..Grant::default()
        },
    };
    let msg = e.to_string();
    assert!(msg.contains('7'), "{msg}");
    assert!(msg.contains("nonprod"), "{msg}");
}

#[test]
fn slot_collision_is_error() {
    let err: Box<dyn std::error::Error> = Box::new(SlotCollisionError::default());
    assert!(!err.to_string().is_empty());
}
