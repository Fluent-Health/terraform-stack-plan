// Ported from internal/server/pubsub_test.go and cloudbuildpush_test.go.
use std::sync::Arc;

use base64::Engine as _;
use serde_json::{Value, json};
use tfstackplan_gate::events::{Init, StackState};
use tfstackplan_gate::reconcile::{RunRequested, RunStartResult};
use tfstackplan_server_clients::github::MockGitHub;

use crate::testutil::{
    cloud_build_push, fake_oidc, push_verifier_app as run_trigger_app, seed_init, send, store,
};
use crate::{App, Config};

/// Go's `pushBody`: a Pub/Sub envelope around `data` (empty data for `None`).
fn push_body(data: Option<Value>) -> String {
    let raw = data.map(|d| d.to_string()).unwrap_or_default();
    json!({"message": {"data": base64::engine::general_purpose::STANDARD.encode(raw)}, "subscription": "s"})
        .to_string()
}

async fn push(app: &Arc<App>, path: &str, token: &str, body: &str) -> u16 {
    send(app, "POST", path, token, body).await.status
}

// --- pubsub_test.go ---

#[tokio::test]
async fn push_event_verifies() {
    let (_d, s) = store();
    let mut a = App::new(
        s,
        Arc::new(MockGitHub::default()),
        Config {
            push_service_account: "pusher@x.iam.gserviceaccount.com".into(),
            ..Config::default()
        },
    );
    a.push_verifier = Some(fake_oidc(&[("good", "pusher@x.iam.gserviceaccount.com")]));
    let a = Arc::new(a);
    seed_init(
        &a,
        Init {
            id: "e1".into(),
            repo: "o/r".into(),
            stacks: vec![StackState {
                path: "s/a".into(),
                ..StackState::default()
            }],
            ..Init::default()
        },
    )
    .await;
    assert_eq!(
        push(
            &a,
            "/pubsub/push",
            "good",
            &push_body(Some(json!({"id": "e1"})))
        )
        .await,
        204,
        "valid push"
    );
    assert_eq!(
        push(&a, "/pubsub/push", "", &push_body(None)).await,
        401,
        "missing token"
    );
    assert_eq!(
        push(&a, "/pubsub/push", "bad", &push_body(None)).await,
        401,
        "bad token"
    );
    assert_eq!(
        push(&a, "/pubsub/push", "good", "not json").await,
        400,
        "bad envelope"
    );
}

#[tokio::test]
async fn push_event_wrong_email() {
    let (_d, s) = store();
    let mut a = App::new(
        s,
        Arc::new(MockGitHub::default()),
        Config {
            push_service_account: "allowed@x".into(),
            ..Config::default()
        },
    );
    a.push_verifier = Some(crate::testutil::verifier_for("intruder@y"));
    let a = Arc::new(a);
    assert_eq!(push(&a, "/pubsub/push", "x", &push_body(None)).await, 403);
}

#[tokio::test]
async fn push_event_disabled_without_verifier() {
    let (_d, a) = crate::testutil::app();
    assert_eq!(push(&a, "/pubsub/push", "", &push_body(None)).await, 404);
}

// --- cloudbuildpush_test.go ---

const PLAN_TRIGGER: &[(&str, &str)] = &[("nonprod-plan", "plan")];

#[tokio::test]
async fn cloud_build_push_adopts_rebuild() {
    let (_d, a) = run_trigger_app("cb-push@sa", "", PLAN_TRIGGER);
    let repo = "o/r";
    a.handle(
        30,
        "nonprod",
        repo,
        RunRequested {
            kind: "plan".into(),
            sha: "caf00dcafe00".into(),
            branch: "feat/x".into(),
            ..RunRequested::default()
        }
        .into(),
    )
    .await
    .expect("run requested");
    let old_id = a
        .store()
        .latest_execution_id(30, "nonprod")
        .expect("latest")
        .expect("no queued execution");
    a.handle(
        30,
        "nonprod",
        repo,
        RunStartResult {
            kind: "plan".into(),
            execution_id: old_id.clone(),
            err: "build failed before the runner reported".into(),
            ..RunStartResult::default()
        }
        .into(),
    )
    .await
    .expect("start result");

    let code = cloud_build_push(
        &a,
        &json!({
            "id": "build-rerun-999",
            "status": "SUCCESS",
            "substitutions": {"TRIGGER_NAME": "nonprod-plan", "COMMIT_SHA": "caf00dcafe00", "_PR_NUMBER": "30"},
        }),
    )
    .await;
    assert_eq!(code, 204);
    let old = a
        .store()
        .get_execution(&old_id)
        .expect("get")
        .expect("old row");
    assert!(
        !old.superseded_by.is_empty(),
        "old run not superseded: {old:?}"
    );
    assert!(
        a.store()
            .get_execution(&old.superseded_by)
            .expect("get")
            .is_some(),
        "adopted execution {} missing",
        old.superseded_by
    );
}

#[tokio::test]
async fn cloud_build_push_ignores_foreign_trigger() {
    let (_d, a) = run_trigger_app("cb-push@sa", "", PLAN_TRIGGER);
    let code = cloud_build_push(
        &a,
        &json!({"id": "someone-elses-build", "status": "SUCCESS",
                "substitutions": {"TRIGGER_NAME": "unrelated-service-build", "COMMIT_SHA": "x"}}),
    )
    .await;
    assert_eq!(code, 204, "acked, but no-op");
}

#[tokio::test]
async fn cloud_build_push_disabled_returns_404() {
    let (_d, a) = run_trigger_app("cb-push@sa", "", &[]);
    let code = cloud_build_push(&a, &json!({"id": "b", "status": "SUCCESS"})).await;
    assert_eq!(code, 404);
}

#[tokio::test]
async fn cloud_build_push_missing_bearer_returns_401() {
    let (_d, a) = run_trigger_app("cb-push@sa", "", PLAN_TRIGGER);
    assert_eq!(push(&a, "/pubsub/cloud-builds", "", "{}").await, 401);
}

#[tokio::test]
async fn cloud_build_push_wrong_identity_returns_403() {
    let (_d, a) = run_trigger_app("intruder@sa", "cb-push@sa", PLAN_TRIGGER);
    let code = cloud_build_push(&a, &json!({"id": "b", "status": "SUCCESS"})).await;
    assert_eq!(code, 403);
}

#[tokio::test]
async fn cloud_build_push_malformed_envelope_acks() {
    let (_d, a) = run_trigger_app("cb-push@sa", "", PLAN_TRIGGER);
    assert_eq!(
        push(&a, "/pubsub/cloud-builds", "faketoken", "not json").await,
        204
    );
}
