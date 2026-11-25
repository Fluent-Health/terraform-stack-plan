// Tests are added as the Go cases are ported.

#[test]
fn exec_context_kinds() {
    assert_eq!(super::exec_context_kind("plan/staging"), "plan");
    assert_eq!(super::exec_context_kind("apply"), "apply");
    assert_eq!(super::exec_context_kind("verify/x"), "verify");
    assert_eq!(super::exec_context_kind("iam/x"), "other");
}

#[tokio::test]
async fn phases_seeded_through_the_aggregate_render() {
    use tfstackplan_gate::events::{Init, Phase, PhaseEvent};
    let (_d, a) = crate::testutil::app();
    crate::testutil::seed_init(
        &a,
        Init {
            id: "p1".into(),
            pr: 3,
            context: "plan".into(),
            ..Init::default()
        },
    )
    .await;
    crate::testutil::seed_phase(
        &a,
        PhaseEvent {
            id: "p1".into(),
            phase: Phase::new("planning"),
            ..PhaseEvent::default()
        },
    )
    .await;
    let r = crate::testutil::send(&a, "GET", "/api/lifecycle?pr=3", "", "").await;
    assert_eq!(r.status, 200);
    assert!(r.text().contains(r#""key":"plan""#), "{}", r.text());
}
