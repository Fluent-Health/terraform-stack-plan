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
    ] {
        assert!(!s.open(), "{s:?} should be closed");
    }
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
