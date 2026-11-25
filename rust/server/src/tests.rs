// Ported from internal/server/wirecompat_test.go: every case sends a fixed request
// and byte-compares status, Content-Type and body against Go's committed goldens
// in internal/server/testdata/wire (read in place, not copied).
use std::sync::Arc;

use tfstackplan_gate::events::{Init, StackState, Status};
use tfstackplan_server_clients::github::MockGitHub;
use tfstackplan_store::PrMeta;

use crate::testutil::{fake_oidc, seed_init, seed_projection_target, send};
use crate::{App, Config};

/// Go's `fixed` time as its SQLite driver stores a UTC time.Time.
const FIXED: &str = "2030-01-02 03:04:05 +0000 UTC";

fn golden(name: &str) -> Vec<u8> {
    let p = std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../internal/server/testdata/wire")
        .join(format!("{name}.golden"));
    std::fs::read(&p).unwrap_or_else(|e| panic!("read {}: {e}", p.display()))
}

fn render(status: u16, ct: &str, body: &[u8]) -> Vec<u8> {
    let mut out = format!("HTTP {status}\nContent-Type: {ct}\n\n").into_bytes();
    out.extend_from_slice(body);
    out
}

#[tokio::test]
async fn api_wire_compat() {
    let (_dir, s) = crate::testutil::store();
    let mut app = App::new(
        Arc::clone(&s),
        Arc::new(MockGitHub::default()),
        Config {
            api_principals: [(
                "runner@x.iam.gserviceaccount.com".to_string(),
                vec![
                    "report".to_string(),
                    "read".to_string(),
                    "admin".to_string(),
                ],
            )]
            .into(),
            ..Config::default()
        },
    );
    app.api_verifier = Some(fake_oidc(&[
        ("tok", "runner@x.iam.gserviceaccount.com"),
        ("tok-noscopes", "stranger@x.iam.gserviceaccount.com"),
    ]));
    let app = Arc::new(app);

    let steps: &[(&str, &str, &str, &str, &str)] = &[
        (
            "01-init",
            "POST",
            "/api/init",
            "tok",
            r#"{"id":"e1","repo":"o/r","sha":"cafe1234cafe","pr":7,"environment":"staging","log_url":"https://ci/logs/1","stacks":[{"path":"stacks/a","project":"proj-a","status":"pending"},{"path":"stacks/b","project":"proj-b","status":"pending"}],"edges":[{"from":"stacks/a","to":"stacks/b"}]}"#,
        ),
        (
            "02-phase",
            "POST",
            "/api/phase",
            "tok",
            r#"{"id":"e1","phase":"planning"}"#,
        ),
        (
            "03-update",
            "POST",
            "/api/update",
            "tok",
            r#"{"id":"e1","stack":"stacks/a","status":"planned"}"#,
        ),
        (
            "04-update-unknown-status",
            "POST",
            "/api/update",
            "tok",
            r#"{"id":"e1","stack":"stacks/a","status":"bogus"}"#,
        ),
        (
            "05-logs",
            "POST",
            "/api/logs",
            "tok",
            "{\"id\":\"e1\",\"stack\":\"stacks/a\",\"data\":\"terraform plan says hi\\n\"}",
        ),
        (
            "06-finalize",
            "POST",
            "/api/finalize",
            "tok",
            "{\"id\":\"e1\",\"report_markdown\":\"## report\\n\",\"projects\":{\"stacks/a\":\"proj-a\"},\"stack_reports\":{\"stacks/a\":\"### stacks/a\\n\"},\"categories\":{\"stacks/a\":[{\"name\":\"iam\",\"icon\":\"🔐\"}]},\"counts\":{\"stacks/a\":{\"add\":1,\"change\":2}}}",
        ),
        (
            "07-gate-check-clean",
            "POST",
            "/api/gate/check",
            "tok",
            r#"{"pr":7,"environment":"staging"}"#,
        ),
        (
            "08-gate-check-not-classified",
            "POST",
            "/api/gate/check",
            "tok",
            r#"{"pr":99,"environment":"staging"}"#,
        ),
        (
            "09-gate-revoke",
            "POST",
            "/api/gate/revoke",
            "tok",
            r#"{"pr":7,"environment":"staging"}"#,
        ),
        (
            "10-claims-list",
            "POST",
            "/api/claims/list",
            "tok",
            r#"{"environment":"staging"}"#,
        ),
        (
            "11-claims-list-empty",
            "POST",
            "/api/claims/list",
            "tok",
            r#"{"environment":"deserted"}"#,
        ),
        (
            "12-claims-release",
            "POST",
            "/api/claims/release",
            "tok",
            r#"{"environment":"staging","pr":7,"stack":"stacks/a"}"#,
        ),
        ("13-execution-get", "GET", "/api/execution/e2", "tok", ""),
        (
            "14-execution-get-lifecycle",
            "GET",
            "/api/execution/e1",
            "tok",
            "",
        ),
        ("15-execution-404", "GET", "/api/execution/nope", "tok", ""),
        (
            "15b-execution-zero-stacks",
            "GET",
            "/api/execution/e3",
            "tok",
            "",
        ),
        (
            "16-unauthorized",
            "POST",
            "/api/claims/list",
            "",
            r#"{"environment":"staging"}"#,
        ),
        (
            "17-forbidden",
            "POST",
            "/api/claims/list",
            "tok-noscopes",
            r#"{"environment":"staging"}"#,
        ),
        ("18-executions-list", "GET", "/api/executions", "tok", ""),
        (
            "19-executions-pr-filter",
            "GET",
            "/api/executions?pr=7&limit=1",
            "tok",
            "",
        ),
        (
            "20-executions-bad-limit",
            "GET",
            "/api/executions?limit=0",
            "tok",
            "",
        ),
        ("21-approvals", "GET", "/api/approvals", "tok", ""),
        ("22-pr-get", "GET", "/api/pr/7", "tok", ""),
        ("23-pr-404", "GET", "/api/pr/424242", "tok", ""),
        (
            "24-pr-get-meta-only-no-execution",
            "GET",
            "/api/pr/555",
            "tok",
            "",
        ),
    ];

    let mut failures = Vec::new();
    for &(name, method, path, token, body) in steps {
        match name {
            "10-claims-list" => {
                for sp in ["stacks/a", "stacks/b"] {
                    s.exec(|c| {
                        c.execute(
                            "INSERT INTO apply_claims (environment, stack_path, owner_pr, execution_id, expires_at) VALUES (?1,?2,?3,?4,?5)",
                            rusqlite::params!["staging", sp, 7, "e1", FIXED],
                        )
                    })
                    .unwrap();
                }
            }
            "13-execution-get" => {
                seed_init(
                    &app,
                    Init {
                        id: "e2".into(),
                        repo: "o/r".into(),
                        sha: "beef5678beef".into(),
                        pr: 8,
                        environment: "staging".into(),
                        log_url: "https://ci/logs/2".into(),
                        stacks: vec![StackState {
                            path: "stacks/c".into(),
                            project: "proj-c".into(),
                            status: Status::Planned,
                            ..StackState::default()
                        }],
                        ..Init::default()
                    },
                )
                .await;
                seed_projection_target(
                    &s,
                    8,
                    "staging",
                    "iam",
                    "proj-c",
                    "projects/proj-c/locations/global/entitlements/iam/grants/g1",
                    "ACTIVE",
                    "req@x.iam.gserviceaccount.com",
                );
            }
            "15b-execution-zero-stacks" => {
                seed_init(
                    &app,
                    Init {
                        id: "e3".into(),
                        repo: "o/r".into(),
                        sha: "0000aaaa0000".into(),
                        pr: 9,
                        environment: "staging".into(),
                        log_url: "https://ci/logs/3".into(),
                        ..Init::default()
                    },
                )
                .await;
                s.exec(|c| {
                    c.execute(
                        "UPDATE executions SET created_at = ?1 WHERE id = 'e3'",
                        [FIXED],
                    )
                })
                .unwrap();
            }
            "21-approvals" => {
                seed_projection_target(
                    &s,
                    8,
                    "staging",
                    "destructive",
                    "proj-c",
                    "",
                    "AWAITING",
                    "req@x.iam.gserviceaccount.com",
                );
            }
            "22-pr-get" | "24-pr-get-meta-only-no-execution" => {
                let (pr, title, body, head, auto) = if name.starts_with("22") {
                    (
                        7,
                        "Add widget",
                        "Does widget things.",
                        "feature/widget",
                        true,
                    )
                } else {
                    (
                        555,
                        "Pre-execution PR",
                        "Meta arrives before init.",
                        "feature/pre-execution",
                        false,
                    )
                };
                s.upsert_pr_meta(&PrMeta {
                    repo: "o/r".into(),
                    pr,
                    title: title.into(),
                    body: body.into(),
                    author_login: "octocat".into(),
                    head_ref: head.into(),
                    url: format!("https://github.com/o/r/pull/{pr}"),
                    auto_merge: auto,
                    updated_at: None,
                })
                .unwrap();
            }
            _ => {}
        }
        if name.starts_with("13-") || name.starts_with("14-") {
            s.exec(|c| c.execute("UPDATE executions SET created_at = ?1", [FIXED]))
                .unwrap();
        }
        let resp = send(&app, method, path, token, body).await;
        let got = render(resp.status, &resp.content_type, &resp.body);
        let want = golden(name);
        if got != want {
            failures.push(format!(
                "{name}\n--- got ---\n{}\n--- want ---\n{}",
                String::from_utf8_lossy(&got),
                String::from_utf8_lossy(&want)
            ));
        }
    }
    assert!(
        failures.is_empty(),
        "wire mismatches:\n{}",
        failures.join("\n\n")
    );
}

// --- hub_test.go ---

mod hub {
    use std::time::Duration;

    use crate::Hub;

    #[tokio::test]
    async fn hub_publish_reaches_subscriber() {
        let h = Hub::default();
        let mut sub = h.subscribe("e1|a");
        h.publish("e1|a", "hello");
        let got = tokio::time::timeout(Duration::from_secs(1), sub.rx.recv())
            .await
            .expect("subscriber did not receive the chunk");
        assert_eq!(got.as_deref(), Some("hello"));
    }

    #[tokio::test]
    async fn hub_publish_to_other_key_not_received() {
        let h = Hub::default();
        let mut sub = h.subscribe("e1|a");
        h.publish("e1|b", "nope");
        let got = tokio::time::timeout(Duration::from_millis(50), sub.rx.recv()).await;
        assert!(got.is_err(), "received cross-key chunk {got:?}");
    }

    /// Go reads the closed channel after `unsub`; in Rust the receiver goes with
    /// the dropped subscription, so this checks the subscriber is gone and a
    /// publish to its key is a no-op.
    #[test]
    fn hub_unsubscribe_stops_delivery() {
        let h = Hub::default();
        let sub = h.subscribe("e1|a");
        drop(sub);
        h.publish("e1|a", "after-unsub");
        assert!(
            !h.subs.lock().expect("lock").contains_key("e1|a"),
            "subscriber still registered after unsubscribe"
        );
    }

    #[test]
    fn hub_publish_non_blocking_on_full_channel() {
        let h = Hub::default();
        let _sub = h.subscribe("e1|a"); // never drained
        let start = std::time::Instant::now();
        for _ in 0..100_000 {
            h.publish("e1|a", "x");
        }
        assert!(
            start.elapsed() < Duration::from_secs(2),
            "publish blocked on a full subscriber channel"
        );
    }

    #[tokio::test(flavor = "multi_thread", worker_threads = 4)]
    async fn hub_concurrent() {
        let h = std::sync::Arc::new(Hub::default());
        let tasks: Vec<_> = (0..20)
            .map(|_| {
                let h = std::sync::Arc::clone(&h);
                tokio::spawn(async move {
                    let mut sub = h.subscribe("k");
                    h.publish("k", "v");
                    let _ = tokio::time::timeout(Duration::from_secs(1), sub.rx.recv()).await;
                })
            })
            .collect();
        for t in tasks {
            t.await.expect("task");
        }
        assert!(h.subs.lock().expect("lock").is_empty());
    }
}

// --- e2e_test.go ---

mod e2e {
    use std::sync::Arc;

    use serde_json::json;
    use tfstackplan_gate::reconcile::{RunRequested, RunStartResult};
    use tfstackplan_server_clients::github::MockGitHub;

    use crate::testutil::{
        check_updates, cloud_build_push, created_check_runs, push_verifier_app, send, store,
    };
    use crate::{App, Config};

    async fn post(app: &Arc<App>, path: &str, body: serde_json::Value) {
        send(app, "POST", path, "", &body.to_string()).await;
    }

    #[tokio::test]
    async fn e2e_plan_lifecycle() {
        let (_d, s) = store();
        let mut gh = MockGitHub::default();
        gh.create_check_run_fn = Some(Box::new(|_| Ok(42)));
        let gh = Arc::new(gh);
        let a = Arc::new(App::new(
            Arc::clone(&s),
            gh.clone(),
            Config {
                public_base_url: "https://srv".into(),
                ..Config::default()
            },
        ));
        post(&a, "/api/phase", json!({"id":"e1","repo":"o/r","sha":"sha","pr":7,"environment":"staging","phase":"warming"})).await;
        post(
            &a,
            "/api/init",
            json!({"id":"e1","repo":"o/r","sha":"sha","pr":7,"environment":"staging",
            "stacks":[{"path":"a"},{"path":"b"}],"edges":[{"from":"a","to":"b"}]}),
        )
        .await;
        post(
            &a,
            "/api/update",
            json!({"id":"e1","stack":"a","status":"running"}),
        )
        .await;
        post(
            &a,
            "/api/update",
            json!({"id":"e1","stack":"a","status":"planned"}),
        )
        .await;
        post(
            &a,
            "/api/update",
            json!({"id":"e1","stack":"b","status":"planned"}),
        )
        .await;
        post(
            &a,
            "/api/finalize",
            json!({"id":"e1","report_markdown":"# report"}),
        )
        .await;

        let plan_checks = created_check_runs(&gh)
            .iter()
            .filter(|n| !n.starts_with("apply-lock"))
            .count();
        assert_eq!(plan_checks, 1, "plan check runs created");
        let ups = check_updates(&gh);
        let final_concl = ups
            .iter()
            .rfind(|u| !u.conclusion.is_empty())
            .map(|u| u.conclusion.as_str());
        assert_eq!(final_concl, Some("success"));
        assert!(
            ups.len() >= 2,
            "expected several check-run patches, got {}",
            ups.len()
        );
        let e = s.get_execution("e1").expect("get").expect("row");
        assert!(e.report_markdown == "# report" && e.rev != 0, "{e:?}");
    }

    /// Steps 1 and 2 of both inbound-rebuild tests: a serve plan run for PR 44
    /// fails to start, then a rebuild serve did not launch arrives without
    /// `_PR_NUMBER`. Returns (old id, adopted id).
    async fn stuck_then_adopted(a: &Arc<App>, sha: &str) -> (String, String) {
        a.handle(
            44,
            "nonprod",
            "o/r",
            RunRequested {
                kind: "plan".into(),
                sha: sha.into(),
                branch: "feat/x".into(),
                ..RunRequested::default()
            }
            .into(),
        )
        .await
        .expect("run requested");
        let old_id = a
            .store()
            .latest_execution_id(44, "nonprod")
            .expect("latest")
            .expect("no queued execution");
        a.handle(
            44,
            "nonprod",
            "o/r",
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
            a,
            &json!({"id":"build-rerun-ok","status":"WORKING",
                    "substitutions":{"TRIGGER_NAME":"nonprod-plan","COMMIT_SHA":sha}}),
        )
        .await;
        assert_eq!(code, 204, "push");
        let old = a
            .store()
            .get_execution(&old_id)
            .expect("get")
            .expect("old row");
        assert!(
            !old.superseded_by.is_empty(),
            "stuck run not superseded: {old:?}"
        );
        (old_id, old.superseded_by)
    }

    #[tokio::test]
    async fn inbound_rebuild_recovers_stuck_check() {
        let (_d, a) = push_verifier_app("cb-push@sa", "", &[("nonprod-plan", "plan")]);
        let sha = "feedfacefeed00";
        let (_old_id, adopted_id) = stuck_then_adopted(&a, sha).await;
        post(
            &a,
            "/api/init",
            json!({"id":"runner-rerun","repo":"o/r","sha":sha,"pr":0,"environment":"nonprod"}),
        )
        .await;
        let runner = a
            .store()
            .get_execution("runner-rerun")
            .expect("get")
            .expect("runner row");
        assert_eq!(runner.pr, 44, "runner Init did not recover PR: {runner:?}");
        let adopted = a
            .store()
            .get_execution(&adopted_id)
            .expect("get")
            .expect("adopted row");
        assert_eq!(adopted.superseded_by, "runner-rerun", "{adopted:?}");
    }

    #[tokio::test]
    async fn inbound_rebuild_recovers_stuck_check_reused_id() {
        let (_d, a) = push_verifier_app("cb-push@sa", "", &[("nonprod-plan", "plan")]);
        let sha = "feedfacefeed00";
        let (old_id, adopted_id) = stuck_then_adopted(&a, sha).await;
        post(
            &a,
            "/api/init",
            json!({"id":old_id,"repo":"o/r","sha":sha,"pr":0,"environment":"nonprod"}),
        )
        .await;
        let old = a
            .store()
            .get_execution(&old_id)
            .expect("get")
            .expect("old row");
        let adopted = a
            .store()
            .get_execution(&adopted_id)
            .expect("get")
            .expect("adopted row");
        assert_eq!(old.status, "in_progress");
        assert_eq!(old.superseded_by, "");
        assert_eq!(adopted.superseded_by, old_id);
    }
}
