// Ported from internal/server/adminhandlers_test.go.
use std::sync::Arc;

use serde_json::json;
use tfstackplan_gate::approval::{GrantState, Request};
use tfstackplan_gate::events::{GateTarget, Init};
use tfstackplan_gate::reconcile::{
    ChangeSet, Classified, Gate, GateState, GateTick, GrantObserved, ObservedGrant, Pending, Target,
};
use tfstackplan_server_clients::approval::{Backend as _, Fake};
use tfstackplan_server_clients::github::MockGitHub;
use tfstackplan_store::exec_stream_id;

use crate::testutil::{seed_init, send, store};
use crate::{App, Config};

fn req() -> Request {
    Request {
        class: "iam".into(),
        target: "proj-a".into(),
        pr: 7,
        environment: "staging".into(),
        ..Request::default()
    }
}

/// An app whose (7, staging) gate is Pending on an AWAITING grant-1, as both Go
/// admin gate tests seed it.
async fn awaiting_gate() -> (tempfile::TempDir, Arc<App>, Arc<Fake>) {
    let (d, s) = store();
    let mut a = App::new(s, Arc::new(MockGitHub::default()), Config::default());
    let fake = Arc::new(Fake::new());
    a.approval = Some(fake.clone());
    fake.request_grant(&req()).await.expect("request grant");
    let state = ChangeSet {
        pr: 7,
        environment: "staging".into(),
        gate: GateState::Pending(Pending {
            targets: vec![Target {
                class: "iam".into(),
                target: "proj-a".into(),
                grant_name: "grant-1".into(),
                grant: GrantState::Awaiting,
            }],
            ..Pending::default()
        }),
        ..ChangeSet::default()
    };
    a.store()
        .append::<Gate>(
            &exec_stream_id(7, "staging"),
            0,
            &[
                Classified {
                    gates: vec![GateTarget {
                        class: "iam".into(),
                        target: "proj-a".into(),
                    }],
                }
                .into(),
                GrantObserved {
                    class: "iam".into(),
                    target: "proj-a".into(),
                    name: "grant-1".into(),
                    state: GrantState::Awaiting,
                    ..GrantObserved::default()
                }
                .into(),
            ],
            &state,
        )
        .expect("append");
    let a = Arc::new(a);
    let _ = a
        .handle(
            7,
            "staging",
            "fluent/repo",
            GateTick {
                grants: vec![ObservedGrant {
                    class: "iam".into(),
                    target: "proj-a".into(),
                    name: "grant-1".into(),
                    state: GrantState::Awaiting,
                    ..ObservedGrant::default()
                }],
            }
            .into(),
        )
        .await;
    (d, a, fake)
}

fn target_states(a: &App) -> Vec<String> {
    a.store()
        .targets_for(7, "staging")
        .expect("targets")
        .into_iter()
        .map(|t| t.state)
        .collect()
}

#[tokio::test]
async fn admin_grants_release() {
    let (_d, a, _fake) = awaiting_gate().await;
    let body = json!({"pr":7,"environment":"staging","class":"iam","target":"proj-a","reason":"manual release"});
    let w = send(
        &a,
        "POST",
        "/api/admin/grants/release",
        "",
        &body.to_string(),
    )
    .await;
    assert_eq!(w.status, 200, "{}", w.text());
    a.tick(7, "staging").await.expect("tick");
    assert_eq!(target_states(&a), ["REVOKED"]);
}

#[tokio::test]
async fn admin_gates_satisfy() {
    let (_d, a, fake) = awaiting_gate().await;
    let body = json!({"pr":7,"environment":"staging","class":"iam","target":"proj-a","reason":"manual satisfy"});
    let w = send(
        &a,
        "POST",
        "/api/admin/gates/satisfy",
        "",
        &body.to_string(),
    )
    .await;
    assert_eq!(w.status, 200, "{}", w.text());
    fake.approve(&req());
    a.tick(7, "staging").await.expect("tick");
    assert_eq!(target_states(&a), ["ACTIVE"]);
}

#[tokio::test]
async fn admin_checks_override() {
    let (_d, a) = crate::testutil::app();
    let body = json!({"pr":7,"environment":"staging","check":"terraform/staging","conclusion":"success","reason":"bypass"});
    let w = send(
        &a,
        "POST",
        "/api/admin/checks/override",
        "",
        &body.to_string(),
    )
    .await;
    assert_eq!(w.status, 200, "{}", w.text());
    let (cs, _) = a
        .store()
        .load::<Gate>(&exec_stream_id(7, "staging"))
        .expect("load");
    assert_eq!(
        cs.check_override.map(|o| o.conclusion).as_deref(),
        Some("success")
    );
}

#[tokio::test]
async fn admin_executions_cancel() {
    let (_d, a) = crate::testutil::app();
    seed_init(
        &a,
        Init {
            id: "exec-123".into(),
            pr: 7,
            environment: "staging".into(),
            repo: "fluent/repo".into(),
            sha: "abcdef".into(),
            ..Init::default()
        },
    )
    .await;
    let body = json!({"id":"exec-123","reason":"hung build"});
    let w = send(
        &a,
        "POST",
        "/api/admin/executions/cancel",
        "",
        &body.to_string(),
    )
    .await;
    assert_eq!(w.status, 200, "{}", w.text());
    let (cs, _) = a
        .store()
        .load::<Gate>(&exec_stream_id(7, "staging"))
        .expect("load");
    if let Some(run) = cs.runs.get("") {
        assert!(
            !run.live(),
            "expected run to be non-live, got {:?}",
            run.phase
        );
    }
}
