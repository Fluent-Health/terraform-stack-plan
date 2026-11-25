// Ported from internal/server/executionhandlers_test.go.
use std::sync::Arc;
use std::time::Duration;

use serde_json::Value;
use tfstackplan_gate::events::{Init, Phase, PhaseEvent, StackState};
use tfstackplan_server_clients::github::MockGitHub;

use crate::testutil::{
    fake_oidc, open_stream, seed_init, seed_phase, seed_projection_target, send, store,
};
use crate::{App, Config};

const TOK: &str = "tok-runner";

#[tokio::test]
async fn get_execution() {
    let (_d, s) = store();
    let mut a = App::new(
        s,
        Arc::new(MockGitHub::default()),
        Config {
            api_principals: [(
                "runner@x.iam.gserviceaccount.com".to_string(),
                vec!["report".to_string()],
            )]
            .into(),
            ..Config::default()
        },
    );
    a.api_verifier = Some(fake_oidc(&[(TOK, "runner@x.iam.gserviceaccount.com")]));
    let a = Arc::new(a);
    seed_init(
        &a,
        Init {
            id: "e1".into(),
            repo: "o/r".into(),
            sha: "sha123".into(),
            pr: 7,
            environment: "staging".into(),
            stacks: vec![StackState {
                path: "s/a".into(),
                ..StackState::default()
            }],
            ..Init::default()
        },
    )
    .await;
    seed_projection_target(
        a.store(),
        7,
        "staging",
        "gcp-pam",
        "proj-1",
        "grant-1",
        "ACTIVE",
        "requester-1",
    );

    // 1. No token.
    assert_eq!(
        send(&a, "GET", "/api/execution/e1", "", "").await.status,
        401
    );
    // 2. Unknown execution.
    assert_eq!(
        send(&a, "GET", "/api/execution/nonexistent", TOK, "")
            .await
            .status,
        404
    );
    // 3. Success.
    let w = send(&a, "GET", "/api/execution/e1", TOK, "").await;
    assert_eq!(w.status, 200, "{}", w.text());
    let res: Value = serde_json::from_slice(&w.body).expect("json");
    assert!(
        res["ID"] == "e1"
            && res["Repo"] == "o/r"
            && res["SHA"] == "sha123"
            && res["PR"] == 7
            && res["Environment"] == "staging",
        "{res}"
    );
    let stacks = res["graph"]["stacks"].as_array().expect("stacks");
    assert!(stacks.len() == 1 && stacks[0]["path"] == "s/a", "{res}");
    let gates = res["gates"].as_array().expect("gates");
    assert!(
        gates.len() == 1 && gates[0]["Class"] == "gcp-pam" && gates[0]["State"] == "ACTIVE",
        "{res}"
    );
    assert_ne!(res["ProgressPct"], 0, "fallback progress must be non-zero");

    // 3b. Seeded dynamic progress.
    seed_phase(
        &a,
        PhaseEvent {
            id: "e1".into(),
            phase: Phase::new("custom-moves"),
            label: "custom moving...".into(),
            progress_pct: Some(88),
            ..PhaseEvent::default()
        },
    )
    .await;
    let w = send(&a, "GET", "/api/execution/e1", TOK, "").await;
    let res: Value = serde_json::from_slice(&w.body).expect("json");
    assert_eq!(res["ProgressPct"], 88, "{res}");
    assert_eq!(res["ProgressLabel"], "custom moving...", "{res}");

    // 4. Events without a token.
    assert_eq!(
        send(&a, "GET", "/api/execution/e1/events", "", "")
            .await
            .status,
        401
    );

    // 5. The SSE stream.
    let mut st = open_stream(&a, "/api/execution/e1/events", TOK, &[]).await;
    assert_eq!(st.status, 200);
    assert!(
        st.content_type.starts_with("text/event-stream"),
        "{}",
        st.content_type
    );
    let wait = Duration::from_secs(10);
    a.hub.publish("exec:e1", "changed");
    st.wait_for("data: changed\n", wait).await;
    a.hub.publish("exec:e1", "superseded:e2");
    st.wait_for("event: superseded\ndata: e2\n", wait).await;
}
