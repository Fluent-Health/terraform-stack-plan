// Ported from internal/server: github_webhook_test.go and run_trigger_test.go (its
// two watchdog cases are in loops/tests.rs, beside watch_runs_once).
use std::sync::Arc;

use serde_json::json;
use tfstackplan_gate::approval::Request;
use tfstackplan_gate::events::{GateTarget, Init, StackState};
use tfstackplan_gate::reconcile::RunnerFinalize;
use tfstackplan_server_clients::approval::{Backend as _, Fake};
use tfstackplan_server_clients::executor::RunRequest;
use tfstackplan_server_clients::github::MockGitHub;

use super::pr_from_merge_subject;
use crate::testutil::{
    FakeExecutor, WH_SECRET, check_updates, created_check_runs, pr_sync_payload, run_trigger_app,
    run_trigger_gh, seed_init, seed_terminal_status, send, send_with, webhook_req,
};
use crate::{App, Config};

fn webhook_app(secret: &str, approval: Option<Arc<Fake>>) -> (tempfile::TempDir, Arc<App>) {
    let (d, s) = crate::testutil::store();
    let mut a = App::new(
        s,
        Arc::new(MockGitHub::default()),
        Config {
            github_webhook_secret: secret.into(),
            ..Config::default()
        },
    );
    if let Some(f) = approval {
        a.approval = Some(f);
    }
    (d, Arc::new(a))
}

fn open_grant(grants: &[tfstackplan_gate::approval::Grant], pr: i64) -> bool {
    grants.iter().any(|g| g.request.pr == pr && g.state.open())
}

// --- github_webhook_test.go ---

#[tokio::test]
async fn git_hub_webhook_revokes_on_pr_close() {
    let fake = Arc::new(Fake::new());
    let (_d, a) = webhook_app(WH_SECRET, Some(fake.clone()));
    for (id, env, target) in [("e-np", "nonprod", "proj-a"), ("e-pr", "prod", "proj-b")] {
        seed_init(
            &a,
            Init {
                id: id.into(),
                pr: 7,
                environment: env.into(),
                repo: "o/r".into(),
                ..Init::default()
            },
        )
        .await;
        a.handle(
            7,
            env,
            "o/r",
            RunnerFinalize {
                gates: vec![GateTarget {
                    class: "iam".into(),
                    target: target.into(),
                }],
                apply_context: false,
            }
            .into(),
        )
        .await
        .expect("finalize");
    }
    let w = webhook_req(
        &a,
        WH_SECRET,
        "pull_request",
        &json!({"action": "closed", "pull_request": {"number": 7, "merged": false}}),
    )
    .await;
    assert_eq!(w.status, 204);
    for target in ["proj-a", "proj-b"] {
        let grants = fake.list_grants("iam", target).await.expect("list");
        assert!(
            !open_grant(&grants, 7),
            "grant PR 7 iam/{target} still open"
        );
    }
}

#[tokio::test]
async fn git_hub_webhook_leaves_merged_grant() {
    let fake = Arc::new(Fake::new());
    let (_d, a) = webhook_app(WH_SECRET, Some(fake.clone()));
    fake.request_grant(&Request {
        class: "iam".into(),
        target: "proj-a".into(),
        pr: 5,
        environment: "nonprod".into(),
        requester: String::new(),
    })
    .await
    .expect("request");
    let w = webhook_req(
        &a,
        WH_SECRET,
        "pull_request",
        &json!({"action": "closed", "pull_request": {"number": 5, "merged": true}}),
    )
    .await;
    assert_eq!(w.status, 204);
    let grants = fake.list_grants("iam", "proj-a").await.expect("list");
    assert!(open_grant(&grants, 5), "merged PR 5 grant must remain open");
}

#[tokio::test]
async fn git_hub_webhook_persists_pr_meta_on_opened() {
    let (_d, a) = webhook_app("s", None);
    let payload = json!({
        "action": "opened",
        "pull_request": {
            "number": 3, "merged": false, "title": "Add widget", "body": "Some description",
            "user": {"login": "octocat"}, "html_url": "https://github.com/o/r/pull/3",
            "head": {"sha": "abc123", "ref": "feature-branch"},
            "auto_merge": {"enabled_by": {"login": "octocat"}},
        },
        "repository": {"full_name": "o/r"},
    });
    assert_eq!(
        webhook_req(&a, "s", "pull_request", &payload).await.status,
        204
    );
    let meta = a
        .store()
        .get_pr_meta("o/r", 3)
        .expect("get")
        .expect("pr_meta row after opened");
    assert_eq!(meta.title, "Add widget");
    assert_eq!(meta.body, "Some description");
    assert_eq!(meta.author_login, "octocat");
    assert_eq!(meta.head_ref, "feature-branch");
    assert_eq!(meta.url, "https://github.com/o/r/pull/3");
    assert!(meta.auto_merge, "auto_merge object present");
}

#[tokio::test]
async fn git_hub_webhook_persists_pr_meta_auto_merge_null() {
    let (_d, a) = webhook_app("s", None);
    let payload = json!({
        "action": "edited",
        "pull_request": {
            "number": 4, "merged": false, "title": "Fix bug", "body": "",
            "user": {"login": "someone"}, "html_url": "https://github.com/o/r/pull/4",
            "head": {"sha": "def456", "ref": "bugfix"}, "auto_merge": null,
        },
        "repository": {"full_name": "o/r"},
    });
    assert_eq!(
        webhook_req(&a, "s", "pull_request", &payload).await.status,
        204
    );
    let meta = a
        .store()
        .get_pr_meta("o/r", 4)
        .expect("get")
        .expect("pr_meta row after edited");
    assert!(!meta.auto_merge, "auto_merge null");
}

#[tokio::test]
async fn git_hub_webhook_ignores_non_close_actions() {
    let (_d, a) = webhook_app("s", None);
    let w = webhook_req(
        &a,
        "s",
        "pull_request",
        &json!({"action": "opened", "pull_request": {"number": 3, "merged": false}}),
    )
    .await;
    assert_eq!(w.status, 204);
}

#[tokio::test]
async fn git_hub_webhook_ignores_non_pr_events() {
    let (_d, a) = webhook_app("s", None);
    let w = webhook_req(&a, "s", "push", &json!({"ref": "refs/heads/main"})).await;
    assert_eq!(w.status, 204);
}

#[tokio::test]
async fn git_hub_webhook_rejects_bad_signature() {
    let (_d, a) = webhook_app("correct-secret", None);
    let body = json!({"action": "closed", "pull_request": {"number": 1}}).to_string();
    let w = send_with(
        &a,
        "POST",
        "/github/webhook",
        "",
        &body,
        &[
            ("X-GitHub-Event", "pull_request"),
            ("X-Hub-Signature-256", "sha256=badhex"),
        ],
    )
    .await;
    assert_eq!(w.status, 401);
}

#[tokio::test]
async fn git_hub_webhook_disabled_when_no_secret() {
    let (_d, a) = webhook_app("", None);
    let w = send_with(
        &a,
        "POST",
        "/github/webhook",
        "",
        r#"{"action":"closed","pull_request":{"number":1}}"#,
        &[("X-GitHub-Event", "pull_request")],
    )
    .await;
    assert_eq!(w.status, 404);
}

// --- run_trigger_test.go ---

fn starts(fe: &FakeExecutor) -> Vec<RunRequest> {
    fe.starts.lock().expect("lock").clone()
}

fn cancels(fe: &FakeExecutor) -> Vec<String> {
    fe.cancels.lock().expect("lock").clone()
}

fn armed() -> (tempfile::TempDir, Arc<App>, Arc<FakeExecutor>) {
    armed_with(FakeExecutor::default())
}

fn armed_with(fe: FakeExecutor) -> (tempfile::TempDir, Arc<App>, Arc<FakeExecutor>) {
    let fe = Arc::new(fe);
    let (d, a) = run_trigger_app(run_trigger_gh(), fe.clone());
    (d, a, fe)
}

fn failing_executor() -> FakeExecutor {
    FakeExecutor {
        start_err: Some("context deadline exceeded".into()),
        ..FakeExecutor::default()
    }
}

fn push_payload(after: &str, message: &str) -> serde_json::Value {
    json!({
        "ref": "refs/heads/main",
        "after": after,
        "head_commit": {"message": message},
        "repository": {"full_name": "o/r"},
    })
}

fn rerun_payload(name: &str, sha: &str, prs: serde_json::Value) -> serde_json::Value {
    json!({
        "action": "rerequested",
        "check_run": {"name": name, "head_sha": sha, "pull_requests": prs},
        "repository": {"full_name": "o/r"},
    })
}

fn latest(a: &App, pr: i64) -> Option<tfstackplan_store::ExecutionRow> {
    let id = a
        .store()
        .latest_execution_id(pr, "nonprod")
        .expect("latest")?;
    a.store().get_execution(&id).expect("get")
}

#[tokio::test]
async fn webhook_pr_sync_triggers_plan_run() {
    let (_d, a, fe) = armed();
    let w = webhook_req(
        &a,
        WH_SECRET,
        "pull_request",
        &pr_sync_payload(7, "abcdef1234567890"),
    )
    .await;
    assert_eq!(w.status, 204);
    let s = starts(&fe);
    assert_eq!(s.len(), 1, "starts = {s:?}");
    let r = &s[0];
    assert_eq!(
        (
            r.kind.as_str(),
            r.sha.as_str(),
            r.branch.as_str(),
            r.pr,
            r.environment.as_str()
        ),
        ("plan", "abcdef1234567890", "feat/x", 7, "nonprod")
    );
    let e = a
        .store()
        .get_execution(&r.execution_id)
        .expect("get")
        .expect("queued execution missing");
    assert_eq!(e.pr, 7);
    assert_eq!(e.environment, "nonprod");
    assert_eq!(e.sha, "abcdef1234567890");
    assert_eq!(e.status_context, "plan/nonprod");
    assert!(e.check_run_id.is_some_and(|c| c != 0), "no check run");
    webhook_req(
        &a,
        WH_SECRET,
        "pull_request",
        &pr_sync_payload(7, "abcdef1234567890"),
    )
    .await;
    assert_eq!(starts(&fe).len(), 1, "redelivery started another run");
}

#[tokio::test]
async fn webhook_new_sha_supersedes_plan_run() {
    let (_d, a, fe) = armed();
    webhook_req(
        &a,
        WH_SECRET,
        "pull_request",
        &pr_sync_payload(7, "sha-one"),
    )
    .await;
    webhook_req(
        &a,
        WH_SECRET,
        "pull_request",
        &pr_sync_payload(7, "sha-two"),
    )
    .await;
    let s = starts(&fe);
    assert_eq!(s.len(), 2, "starts = {s:?}");
    assert_eq!(cancels(&fe), vec![format!("build-{}", s[0].execution_id)]);
    let old = a
        .store()
        .get_execution(&s[0].execution_id)
        .expect("get")
        .expect("row");
    assert_eq!(old.superseded_by, s[1].execution_id);
}

#[tokio::test]
async fn webhook_push_triggers_apply_run() {
    let (_d, a, fe) = armed();
    let w = webhook_req(
        &a,
        WH_SECRET,
        "push",
        &push_payload("mergesha123456", "feat: something great (#41)\n\nbody text"),
    )
    .await;
    assert_eq!(w.status, 204);
    let s = starts(&fe);
    assert_eq!(s.len(), 1, "starts = {s:?}");
    assert_eq!(
        (s[0].kind.as_str(), s[0].sha.as_str(), s[0].pr),
        ("apply", "mergesha123456", 41)
    );
    webhook_req(
        &a,
        WH_SECRET,
        "push",
        &push_payload("direct999", "hotfix straight to main"),
    )
    .await;
    assert_eq!(starts(&fe).len(), 1, "direct push must not trigger");
}

#[tokio::test]
async fn webhook_check_run_rerequested() {
    let (_d, a, fe) = armed();
    webhook_req(
        &a,
        WH_SECRET,
        "pull_request",
        &pr_sync_payload(7, "sha-one"),
    )
    .await;
    webhook_req(
        &a,
        WH_SECRET,
        "check_run",
        &rerun_payload("plan/nonprod", "sha-one", json!([{"number": 7}])),
    )
    .await;
    let s = starts(&fe);
    assert_eq!(s.len(), 2, "rerun starts a second build: {s:?}");
    assert_ne!(s[1].execution_id, s[0].execution_id, "fresh execution id");
    assert_eq!(cancels(&fe).len(), 1, "rerun of a live run cancels it");
    webhook_req(
        &a,
        WH_SECRET,
        "check_run",
        &rerun_payload("plan/prod", "sha-one", json!([{"number": 7}])),
    )
    .await;
    assert_eq!(starts(&fe).len(), 2, "foreign-tier rerequest ignored");
}

#[tokio::test]
async fn queued_plan_check_uses_consolidated_name() {
    let gh = Arc::new(run_trigger_gh());
    let (_d, s) = crate::testutil::store();
    let mut a = App::new(
        s,
        gh.clone(),
        Config {
            github_webhook_secret: WH_SECRET.into(),
            environment: "nonprod".into(),
            public_base_url: "https://serve.test".into(),
            ..Config::default()
        },
    );
    a.executor = Some(Arc::new(FakeExecutor::default()));
    let a = Arc::new(a);
    webhook_req(
        &a,
        WH_SECRET,
        "pull_request",
        &pr_sync_payload(7, "sha-one"),
    )
    .await;
    let names = created_check_runs(&gh);
    assert_eq!(
        names.first().map(String::as_str),
        Some("terraform/nonprod"),
        "{names:?}"
    );
    let e = latest(&a, 7).expect("row");
    assert_eq!(e.status_context, "plan/nonprod", "identity unchanged");
}

#[tokio::test]
async fn check_run_rerequested_matches_consolidated_name() {
    let (_d, a, fe) = armed();
    webhook_req(
        &a,
        WH_SECRET,
        "pull_request",
        &pr_sync_payload(7, "sha-one"),
    )
    .await;
    webhook_req(
        &a,
        WH_SECRET,
        "check_run",
        &rerun_payload("terraform/nonprod", "sha-one", json!([{"number": 7}])),
    )
    .await;
    assert_eq!(starts(&fe).len(), 2);
}

#[tokio::test]
async fn webhook_run_trigger_disarmed_without_executor() {
    let (_d, s) = crate::testutil::store();
    let a = Arc::new(App::new(
        s,
        Arc::new(MockGitHub::default()),
        Config {
            github_webhook_secret: WH_SECRET.into(),
            environment: "nonprod".into(),
            ..Config::default()
        },
    ));
    let w = webhook_req(
        &a,
        WH_SECRET,
        "pull_request",
        &pr_sync_payload(7, "sha-one"),
    )
    .await;
    assert_eq!(w.status, 204);
    assert!(
        a.store()
            .latest_execution_id(7, "nonprod")
            .expect("latest")
            .is_none(),
        "disarmed serve must not create executions"
    );
}

#[tokio::test]
async fn webhook_start_failure_fails_check() {
    let (_d, a, _fe) = armed_with(failing_executor());
    webhook_req(
        &a,
        WH_SECRET,
        "pull_request",
        &pr_sync_payload(7, "sha-one"),
    )
    .await;
    let e = latest(&a, 7).expect("queued execution exists even when the start fails");
    assert_eq!(e.status, "failure");
}

#[tokio::test]
async fn queued_apply_not_concluded_success() {
    let (_d, a, fe) = armed();
    webhook_req(
        &a,
        WH_SECRET,
        "push",
        &push_payload("mergesha123456", "feat: y (#41)"),
    )
    .await;
    let s = starts(&fe);
    assert_eq!(s.len(), 1, "starts = {s:?}");
    let e = a
        .store()
        .get_execution(&s[0].execution_id)
        .expect("get")
        .expect("row");
    assert_eq!(e.status, "in_progress", "queued apply must not be success");
}

#[tokio::test]
async fn plan_start_failure_concludes_check_failure() {
    let gh = Arc::new(run_trigger_gh());
    let (_d, s) = crate::testutil::store();
    let mut a = App::new(
        s,
        gh.clone(),
        Config {
            github_webhook_secret: WH_SECRET.into(),
            environment: "nonprod".into(),
            public_base_url: "https://serve.test".into(),
            ..Config::default()
        },
    );
    a.executor = Some(Arc::new(failing_executor()));
    let a = Arc::new(a);
    webhook_req(
        &a,
        WH_SECRET,
        "pull_request",
        &pr_sync_payload(7, "sha-one"),
    )
    .await;
    let conclusions: Vec<String> = check_updates(&gh)
        .into_iter()
        .map(|u| u.conclusion)
        .filter(|c| !c.is_empty())
        .collect();
    assert_eq!(
        conclusions.last().map(String::as_str),
        Some("failure"),
        "{conclusions:?}"
    );
    assert_eq!(latest(&a, 7).expect("row").status, "failure");
}

#[tokio::test]
async fn runner_init_supersedes_queued_row() {
    let (_d, a, fe) = armed();
    webhook_req(
        &a,
        WH_SECRET,
        "pull_request",
        &pr_sync_payload(7, "sha-one"),
    )
    .await;
    let queued = starts(&fe)[0].execution_id.clone();
    let body = serde_json::to_string(&Init {
        id: "build-999".into(),
        repo: "o/r".into(),
        sha: "sha-one".into(),
        pr: 7,
        environment: "nonprod".into(),
        ..Init::default()
    })
    .expect("marshal");
    assert_eq!(send(&a, "POST", "/api/init", "", &body).await.status, 200);
    let old = a.store().get_execution(&queued).expect("get").expect("row");
    assert_eq!(old.superseded_by, "build-999");
}

#[tokio::test]
async fn queued_row_does_not_shadow_plan_for_apply_lock() {
    let (_d, a, _fe) = armed();
    seed_init(
        &a,
        Init {
            id: "real-plan".into(),
            repo: "o/r".into(),
            sha: "sha-zero".into(),
            pr: 7,
            environment: "nonprod".into(),
            stacks: ["stacks/a", "stacks/b"]
                .iter()
                .map(|p| StackState {
                    path: (*p).into(),
                    ..StackState::default()
                })
                .collect(),
            ..Init::default()
        },
    )
    .await;
    webhook_req(
        &a,
        WH_SECRET,
        "pull_request",
        &pr_sync_payload(7, "sha-one"),
    )
    .await;
    let stacks = a.pr_changed_stacks("nonprod", 7).await;
    assert_eq!(
        stacks.map(|s| s.len()),
        Some(2),
        "must read the reported plan, not the queued row"
    );
}

#[test]
fn pr_from_merge_subject_cases() {
    for (subject, want) in [
        ("feat: something great (#41)", 41),
        (r#"Revert "fix: allow xmove (#179)" (#190)"#, 190),
        ("Merge pull request #77 from Fluent-Health/feat/x", 77),
        ("hotfix straight to main", 0),
        ("fix: mention (#12) in the middle without trailing pr", 0),
    ] {
        assert_eq!(pr_from_merge_subject(subject), want, "{subject:?}");
    }
}

#[tokio::test]
async fn apply_check_rerun_recovers_pr_from_store() {
    let (_d, a, fe) = armed();
    webhook_req(
        &a,
        WH_SECRET,
        "push",
        &push_payload("mergesha123456", "feat: y (#41)"),
    )
    .await;
    a.handle(
        41,
        "nonprod",
        "o/r",
        RunnerFinalize {
            gates: vec![],
            apply_context: true,
        }
        .into(),
    )
    .await
    .expect("finalize");
    webhook_req(
        &a,
        WH_SECRET,
        "check_run",
        &rerun_payload("apply/nonprod", "mergesha123456", json!([])),
    )
    .await;
    let s = starts(&fe);
    assert_eq!(s.len(), 2, "apply rerun starts a second build: {s:?}");
    assert_eq!((s[1].kind.as_str(), s[1].pr), ("apply", 41));
}

#[tokio::test]
async fn runner_recovery_after_false_start_failure() {
    let (_d, a, _fe) = armed_with(failing_executor());
    webhook_req(
        &a,
        WH_SECRET,
        "pull_request",
        &pr_sync_payload(7, "sha-one"),
    )
    .await;
    let id = a
        .store()
        .latest_execution_id(7, "nonprod")
        .expect("latest")
        .expect("id");
    assert_eq!(
        latest(&a, 7).expect("row").status,
        "failure",
        "precondition"
    );
    a.handle_exec(
        &id,
        tfstackplan_gate::execution::ReportInit {
            exec: tfstackplan_gate::execution::State {
                id: id.clone(),
                pr: 7,
                environment: "nonprod".into(),
                repo: "o/r".into(),
                status: "in_progress".into(),
                ..tfstackplan_gate::execution::State::default()
            },
        }
        .into(),
    )
    .await
    .expect("revive");
    a.handle(7, "nonprod", "o/r", RunnerFinalize::default().into())
        .await
        .expect("finalize");
    a.handle(
        7,
        "nonprod",
        "o/r",
        tfstackplan_gate::reconcile::GateTick::default().into(),
    )
    .await
    .expect("tick");
    let e = a.store().get_execution(&id).expect("get").expect("row");
    assert_ne!(e.status, "failure", "stale start-failure re-applied");
}

#[tokio::test]
async fn check_suite_rerequested_reruns_only_failed_runs() {
    let (_d, a, fe) = armed();
    webhook_req(
        &a,
        WH_SECRET,
        "pull_request",
        &pr_sync_payload(7, "sha-one"),
    )
    .await;
    assert_eq!(starts(&fe).len(), 1, "setup");
    let id = a
        .store()
        .latest_execution_id(7, "nonprod")
        .expect("latest")
        .expect("id");
    let suite = json!({
        "action": "rerequested",
        "check_suite": {"head_sha": "sha-one"},
        "repository": {"full_name": "o/r"},
    });
    webhook_req(&a, WH_SECRET, "check_suite", &suite).await;
    assert_eq!(starts(&fe).len(), 1, "non-failed run must not re-run");
    seed_terminal_status(&a, &id, "failure").await;
    webhook_req(&a, WH_SECRET, "check_suite", &suite).await;
    assert_eq!(starts(&fe).len(), 2, "failed run re-runs");
}
