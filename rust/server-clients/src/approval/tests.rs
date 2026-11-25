use super::*;

// approval_test.go

#[test]
fn grant_state_open() {
    for s in [
        GrantState::Awaiting,
        GrantState::Activating,
        GrantState::Active,
    ] {
        assert!(s.open(), "{s:?}");
    }
    for s in [
        GrantState::Denied,
        GrantState::Revoked,
        GrantState::Expired,
        GrantState::Absent,
    ] {
        assert!(!s.open(), "{s:?}");
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
    assert_eq!(e.to_string(), "grant slot occupied by PR #7 env=nonprod");
    let err: crate::Error = e.into();
    assert!(matches!(err, crate::Error::SlotCollision(_)));
}

// fake_test.go

fn req(target: &str, pr: i64) -> Request {
    Request {
        class: "iam".into(),
        target: target.into(),
        pr,
        environment: "staging".into(),
        requester: String::new(),
    }
}

#[tokio::test]
async fn fake_request_grant_idempotent() {
    let f = Fake::new();
    let r = req("proj-a", 7);
    let g1 = f.request_grant(&r).await.unwrap();
    assert_eq!(g1.state, GrantState::Awaiting);
    let g2 = f.request_grant(&r).await.unwrap();
    assert_eq!(g2.name, g1.name, "re-request must reuse");
}

#[tokio::test]
async fn fake_list_filters_by_class_target() {
    let f = Fake::new();
    f.request_grant(&req("proj-a", 7)).await.unwrap();
    f.request_grant(&req("proj-b", 7)).await.unwrap();
    let got = f.list_grants("iam", "proj-a").await.unwrap();
    assert_eq!(got.len(), 1);
    assert_eq!(got[0].request.target, "proj-a");
}

#[tokio::test]
async fn fake_approve_and_revoke() {
    let f = Fake::new();
    let r = req("proj-a", 7);
    f.request_grant(&r).await.unwrap();
    f.approve(&r);
    assert_eq!(
        f.list_grants("iam", "proj-a").await.unwrap()[0].state,
        GrantState::Active
    );
    f.revoke(&r).await.unwrap();
    assert_eq!(
        f.list_grants("iam", "proj-a").await.unwrap()[0].state,
        GrantState::Revoked
    );
}

#[test]
fn fake_implements_backend() {
    fn assert_impl<T: Backend>() {}
    assert_impl::<Fake>();
}

#[tokio::test]
async fn fake_pool_lease() {
    // The pool lease picks the first unused identity.
    let f = Fake::with_pool(vec!["sa0".into(), "sa1".into()]);
    let mut r = req("proj-a", 1);
    r.environment = "e".into();
    assert_eq!(f.request_grant(&r).await.unwrap().requester, "sa0");
    r.target = "proj-b".into();
    assert_eq!(
        f.request_grant(&r).await.unwrap().requester,
        "sa1",
        "sa0 in use"
    );

    // A requester hint is honoured.
    let f = Fake::with_pool(vec!["sa0".into(), "sa1".into()]);
    let mut r = req("proj-a", 2);
    r.requester = "sa0".into();
    assert_eq!(f.request_grant(&r).await.unwrap().requester, "sa0");
    r.target = "proj-b".into();
    assert_eq!(f.request_grant(&r).await.unwrap().requester, "sa0");

    // An empty pool means no requester.
    let f = Fake::new();
    assert_eq!(
        f.request_grant(&req("proj-a", 3)).await.unwrap().requester,
        ""
    );
}
