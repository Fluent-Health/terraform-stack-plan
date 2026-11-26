// Ported from internal/server/checkrun_test.go, checkrun_apply_test.go,
// consolidated_check_test.go and TestLoadSnapshot of status_test.go.
use std::sync::Arc;
use std::sync::atomic::{AtomicBool, Ordering};

use serde_json::json;
use tfstackplan_gate::approval::Request;
use tfstackplan_gate::claims::AcquireClaim;
use tfstackplan_gate::events::{Counts, Graph, Init, Phase, StackState, Status};
use tfstackplan_gate::reconcile::RunnerFinalize;
use tfstackplan_server_clients::approval::Fake;
use tfstackplan_server_clients::github::{CheckRunUpdate, MockGitHub};
use tfstackplan_store::ApplyLockCheck;

use super::*;
use crate::config::{PhaseWeight, ProgressConfig};
use crate::render::{progress_title, run_progress, terminal_summary};
use crate::testutil::{
    FakeExecutor, WH_SECRET, check_updates, created_check_runs, pr_sync_payload, seed_init,
    seed_projection_target, seed_terminal_status, send, store, webhook_req,
};
use crate::{App, Config};

fn st(path: &str, status: Status) -> StackState {
    StackState {
        path: path.into(),
        status,
        ..StackState::default()
    }
}

fn serve_example() -> Config {
    Config {
        public_base_url: "https://serve.example".into(),
        ..Config::default()
    }
}

/// An app over a fresh store with the given mock; the mock stays inspectable.
fn app_over(gh: MockGitHub, cfg: Config) -> (tempfile::TempDir, Arc<App>, Arc<MockGitHub>) {
    let gh = Arc::new(gh);
    let (dir, s) = store();
    let app = Arc::new(App::new(s, gh.clone(), cfg));
    (dir, app, gh)
}

fn returns_id(id: i64) -> MockGitHub {
    let mut m = MockGitHub::default();
    m.create_check_run_fn = Some(Box::new(move |_| Ok(id)));
    m
}

async fn post_ok(app: &Arc<App>, path: &str, body: serde_json::Value) {
    let w = send(app, "POST", path, "", &body.to_string()).await;
    assert_eq!(w.status, 200, "{path}: {}", w.text());
}

fn last(updates: &[CheckRunUpdate]) -> &CheckRunUpdate {
    updates.last().expect("no check updates recorded")
}

// --- checkrun_test.go ---

#[tokio::test]
async fn plan_check_run_summary_and_title() {
    let (_d, app, gh) = app_over(returns_id(11), serve_example());
    post_ok(
        &app,
        "/api/init",
        json!({"id":"plan-1","repo":"o/r","sha":"abc","pr":3,"environment":"nonprod",
               "stacks":[{"path":"svc/a","status":"pending"}]}),
    )
    .await;
    post_ok(
        &app,
        "/api/update",
        json!({"id":"plan-1","stack":"svc/a","status":"planned"}),
    )
    .await;
    let ups = check_updates(&gh);
    let u = last(&ups);
    for want in ["⠿", "planned"] {
        assert!(
            u.title.contains(want),
            "title missing {want:?}: {:?}",
            u.title
        );
    }
    assert!(!u.summary.starts_with("## "), "{}", u.summary);
    for want in ["| Stack | Ops | Risk | State |", "`svc/a`"] {
        assert!(
            u.summary.contains(want),
            "summary missing {want:?}:\n{}",
            u.summary
        );
    }
}

#[test]
fn progress_title_terminal_shows_counts() {
    let mut a = st("a", Status::Planned);
    a.counts = Some(Counts {
        add: 6,
        ..Counts::default()
    });
    let mut b = st("b", Status::Planned);
    b.counts = Some(Counts {
        change: 3,
        destroy: 2,
        ..Counts::default()
    });
    let got = progress_title(None, &Phase::new(Phase::PLANNING), &[a, b], true, "plan");
    for want in ["+6", "~3", "−2", "2 stacks"] {
        assert!(
            got.contains(want),
            "terminal title {got:?} missing {want:?}"
        );
    }
    assert!(!got.contains('⠿') && !got.contains('⠀'), "{got:?}");
}

#[test]
fn progress_title_terminal_no_changes() {
    let got = progress_title(None, &Phase::new(Phase::PLANNING), &[], true, "plan");
    assert!(got.contains("no changes"), "{got:?}");
}

#[test]
fn progress_title_running_still_has_bar() {
    let got = progress_title(
        None,
        &Phase::new(Phase::PLANNING),
        &[st("a", Status::Running)],
        false,
        "plan",
    );
    assert!(got.contains('⠿'), "{got:?}");
}

fn pending(n: usize) -> Vec<StackState> {
    (0..n)
        .map(|i| st(&format!("svc/{i}"), Status::Pending))
        .collect()
}

#[test]
fn progress_title_no_double_count() {
    let mut stacks = pending(4);
    stacks[0].status = Status::Safe;
    let got = progress_title(None, &Phase::new(Phase::APPLYING), &stacks, false, "apply");
    assert_eq!(got.matches("1/4").count(), 1, "{got:?}");
    assert!(got.contains("applying 1/4"), "{got:?}");
}

#[test]
fn progress_title_preparing() {
    let mut stacks = pending(8);
    for s in &mut stacks[..3] {
        s.status = Status::Planned;
    }
    let got = progress_title(
        None,
        &Phase::new(Phase::INITIALIZING),
        &stacks,
        false,
        "apply",
    );
    assert!(got.contains("3/8"), "{got:?}");
    assert!(got.contains("preparing"), "{got:?}");
    assert!(!got.contains("initializing"), "{got:?}");

    let all8 = pending(8);
    let got2 = progress_title(None, &Phase::new(Phase::PLANNING), &all8, false, "apply");
    assert!(
        got2.contains("preparing") && got2.contains("0/8"),
        "{got2:?}"
    );

    let got3 = progress_title(None, &Phase::new(Phase::APPLYING), &stacks, false, "apply");
    assert!(
        got3.contains("applying") && !got3.contains("preparing"),
        "{got3:?}"
    );

    let got4 = progress_title(None, &Phase::new(Phase::INITIALIZING), &all8, false, "plan");
    assert!(
        got4.contains("initializing") && !got4.contains("preparing"),
        "{got4:?}"
    );
}

#[test]
fn run_progress_preparing_uses_unified_weighted_bar() {
    let prog = ProgressConfig {
        apply: [
            (Phase::WARMING, 1.0),
            (Phase::INITIALIZING, 2.0),
            (Phase::APPLYING, 10.0),
            (Phase::VERIFYING, 1.0),
        ]
        .into_iter()
        .map(|(p, weight)| PhaseWeight {
            phase: Phase::new(p),
            weight,
        })
        .collect(),
        ..ProgressConfig::default()
    };
    let three = |s| vec![st("a", s), st("b", s), st("c", s)];
    let (_, pct, label) = run_progress(
        Some(&prog),
        &Phase::new(Phase::WARMING),
        &three(Status::Pending),
        "apply",
    );
    assert_eq!(pct, 7, "warming band; label={label:?}");
    let (_, pct, label) = run_progress(
        Some(&prog),
        &Phase::new(Phase::INITIALIZING),
        &three(Status::Initialized),
        "apply",
    );
    assert_eq!(pct, 21, "warming+init band");
    assert!(label.contains("preparing"), "{label:?}");
}

#[test]
fn backfill_prefers_log_error_tail() {
    let (_d, s) = store();
    s.upsert_stack_output(
        "exec-detail",
        "a",
        "log",
        "",
        "module.x: Creating...\n╷\n│ Error: googleapi: Error 403: permission denied\n╵\n",
    )
    .expect("upsert");
    let mut g = Graph {
        stacks: vec![StackState {
            detail: "terraform apply failed".into(),
            ..st("a", Status::Failed)
        }],
        ..Graph::default()
    };
    backfill_failure_detail(&s, "exec-detail", &mut g);
    assert!(
        g.stacks[0].detail.contains("Error 403"),
        "{:?}",
        g.stacks[0].detail
    );
}

#[test]
fn backfill_failure_detail_from_log() {
    let (_d, s) = store();
    s.upsert_stack_output(
        "e1",
        "svc/x",
        "log",
        "",
        "applying...\n╷\n│ Error: googleapi 403 setIamPolicy\n╵\n",
    )
    .expect("upsert");
    let mut g = Graph {
        stacks: vec![
            st("svc/x", Status::Failed),
            StackState {
                detail: "kept".into(),
                ..st("svc/y", Status::Failed)
            },
            st("svc/z", Status::Failed),
        ],
        ..Graph::default()
    };
    backfill_failure_detail(&s, "e1", &mut g);
    assert!(
        g.stacks[0]
            .detail
            .contains("Error: googleapi 403 setIamPolicy"),
        "{:?}",
        g.stacks[0].detail
    );
    assert_eq!(g.stacks[1].detail, "kept");
    assert_eq!(g.stacks[2].detail, "");
}

#[test]
fn terminal_summary_apply_tallies() {
    let mut a = st("a", Status::Safe);
    a.counts = Some(Counts {
        change: 20,
        ..Counts::default()
    });
    let stacks = [
        a,
        st("b", Status::Nochange),
        st("c", Status::Failed),
        st("d", Status::Aborted),
    ];
    let got = terminal_summary("Apply", &stacks);
    for want in ["applied 2/4", "1 no-change", "1 failed", "1 aborted"] {
        assert!(got.contains(want), "{got:?} missing {want:?}");
    }
}

// --- status_test.go ---

#[tokio::test]
async fn load_snapshot_reads_stacks_and_gates() {
    let (_d, app, _gh) = app_over(MockGitHub::default(), Config::default());
    seed_init(
        &app,
        Init {
            id: "e1".into(),
            repo: "o/r".into(),
            sha: "sha".into(),
            pr: 7,
            environment: "staging".into(),
            stacks: vec![
                st("a", Status::Planned),
                st("b", Status::Failed),
                st("c", Status::Unset),
            ],
            ..Init::default()
        },
    )
    .await;
    let s = app.store();
    s.set_report("e1", "# report").expect("report");
    seed_projection_target(s, 7, "staging", "iam", "proj-a", "g1", "ACTIVE", "");
    seed_projection_target(s, 7, "staging", "iam", "proj-b", "g2", "AWAITING", "");
    let (snap, exec) = load_snapshot(s, "e1").expect("load").expect("ok");
    assert_eq!(
        (exec.repo.as_str(), exec.pr, exec.environment.as_str()),
        ("o/r", 7, "staging")
    );
    assert_eq!(snap.total_stacks, 3);
    assert_eq!(snap.planned_stacks, 2);
    assert!(snap.any_failed && snap.finalized, "{snap:?}");
    assert_eq!((snap.total_gates, snap.active_gates), (2, 1));
}

// --- checkrun_apply_test.go ---

#[tokio::test]
async fn apply_init_creates_apply_check_run() {
    let (_d, app, gh) = app_over(returns_id(42), serve_example());
    post_ok(
        &app,
        "/api/init",
        json!({"id":"apply-1","repo":"o/r","sha":"deadbeef","pr":7,"environment":"nonprod",
               "context":"apply/nonprod","stacks":[{"path":"a","status":"pending"}]}),
    )
    .await;
    assert_eq!(created_check_runs(&gh), ["apply/nonprod"]);
}

#[tokio::test]
async fn apply_drive_updates_check_run() {
    let (_d, app, gh) = app_over(returns_id(99), serve_example());
    post_ok(
        &app,
        "/api/init",
        json!({"id":"apply-2","repo":"o/r","sha":"abc","pr":0,"environment":"nonprod",
               "context":"apply/nonprod","stacks":[{"path":"a","status":"pending"}]}),
    )
    .await;
    post_ok(
        &app,
        "/api/phase",
        json!({"id":"apply-2","phase":"applying"}),
    )
    .await;
    post_ok(
        &app,
        "/api/update",
        json!({"id":"apply-2","stack":"a","status":"safe"}),
    )
    .await;
    let calls = gh.calls();
    let (id, u) = calls
        .iter()
        .rev()
        .find_map(|c| match c {
            tfstackplan_server_clients::github::Call::UpdateCheckRun {
                check_run_id,
                update,
                ..
            } => Some((*check_run_id, update.clone())),
            _ => None,
        })
        .expect("UpdateCheckRun not called");
    assert_eq!(id, 99);
    assert!(
        !u.title.contains('⠿') && !u.title.contains('⠀'),
        "terminal apply title must not carry the bar: {:?}",
        u.title
    );
    assert!(u.title.contains("applied"), "{:?}", u.title);
    assert!(!u.summary.starts_with("## "), "{}", u.summary);
}

#[tokio::test]
async fn apply_drive_failure_surfaces_stack_and_next_steps() {
    let (_d, app, gh) = app_over(returns_id(5), serve_example());
    post_ok(
        &app,
        "/api/init",
        json!({"id":"apply-fail","repo":"o/r","sha":"abc","pr":0,"environment":"prod",
               "context":"apply/prod","stacks":[{"path":"cluster/fh-prod","status":"pending"}]}),
    )
    .await;
    post_ok(
        &app,
        "/api/update",
        json!({"id":"apply-fail","stack":"cluster/fh-prod","status":"failed","detail":"terraform apply failed"}),
    )
    .await;
    let ups = check_updates(&gh);
    let u = last(&ups);
    assert_eq!(u.conclusion, "failure");
    assert!(
        u.summary.to_lowercase().contains("re-run"),
        "{:?}",
        u.summary
    );
    for want in [
        "cluster/fh-prod",
        "terraform apply failed",
        "/logs/apply-fail/cluster/fh-prod",
    ] {
        assert!(u.text.contains(want), "text missing {want:?}:\n{}", u.text);
    }
}

#[tokio::test]
async fn apply_init_zero_stacks_resolves_success() {
    let (_d, app, gh) = app_over(returns_id(7), serve_example());
    post_ok(
        &app,
        "/api/init",
        json!({"id":"apply-noop","repo":"o/r","sha":"abc","pr":9,"environment":"nonprod",
               "context":"apply/nonprod","stacks":[]}),
    )
    .await;
    assert!(
        !gh.calls().iter().any(|c| matches!(
            c,
            tfstackplan_server_clients::github::Call::PostStatus { .. }
        )),
        "PostStatus must not be called when a check run exists"
    );
    let ups = check_updates(&gh);
    assert_eq!(last(&ups).conclusion, "success");
}

async fn apply_persists(status: Status, want: &str) {
    let (_d, app, _gh) = app_over(returns_id(11), serve_example());
    post_ok(
        &app,
        "/api/init",
        json!({"id":"apply-persist","repo":"o/r","sha":"abc","pr":0,"environment":"nonprod",
               "context":"apply/nonprod","stacks":[{"path":"a","status":"pending"}]}),
    )
    .await;
    post_ok(
        &app,
        "/api/update",
        json!({"id":"apply-persist","stack":"a","status":status.as_str(),"detail":"boom"}),
    )
    .await;
    let e = app
        .store()
        .get_execution("apply-persist")
        .expect("get")
        .expect("row");
    assert_eq!(e.status, want);
}

#[tokio::test]
async fn apply_drive_persists_success_status() {
    apply_persists(Status::Safe, "success").await;
}

#[tokio::test]
async fn apply_drive_persists_failure_status() {
    apply_persists(Status::Failed, "failure").await;
}

#[tokio::test]
async fn drive_apply_verdict() {
    let cases: &[(&str, &str, &[Status], &str)] = &[
        (
            "all applied/nochange",
            "",
            &[Status::Safe, Status::Nochange],
            "success",
        ),
        (
            "failure flag, none failed-per-stack",
            "failure",
            &[Status::Aborted, Status::Aborted],
            "failure",
        ),
        ("one failed", "", &[Status::Failed, Status::Safe], "failure"),
        (
            "all moving",
            "",
            &[Status::Moving, Status::Moving],
            "success",
        ),
    ];
    for (name, exec_state, stacks, want) in cases {
        let (_d, app, gh) = app_over(returns_id(77), serve_example());
        let id = format!("verdict-{name}");
        seed_init(
            &app,
            Init {
                id: id.clone(),
                repo: "o/r".into(),
                context: "apply/nonprod".into(),
                environment: "nonprod".into(),
                stacks: stacks
                    .iter()
                    .enumerate()
                    .map(|(i, s)| st(&format!("s{i}"), *s))
                    .collect(),
                ..Init::default()
            },
        )
        .await;
        app.store().set_check_run_id(&id, 77).expect("check run id");
        if !exec_state.is_empty() {
            seed_terminal_status(&app, &id, exec_state).await;
        }
        let e = app.store().get_execution(&id).expect("get").expect("row");
        app.drive_apply(e, "http://x").await;
        let ups = check_updates(&gh);
        assert_eq!(last(&ups).conclusion, *want, "{name}");
    }
}

// --- consolidated_check_test.go ---

struct Consolidated {
    _dir: tempfile::TempDir,
    app: Arc<App>,
    gh: Arc<MockGitHub>,
    fe: Arc<FakeExecutor>,
}

fn armed_config() -> Config {
    Config {
        github_webhook_secret: WH_SECRET.into(),
        environment: "nonprod".into(),
        public_base_url: "https://serve.test".into(),
        ..Config::default()
    }
}

/// Go's `consolidatedApp`: an armed serve whose mock records every check update.
fn consolidated_with(gh: MockGitHub, armed: bool, approval: Option<Arc<Fake>>) -> Consolidated {
    let gh = Arc::new(gh);
    let (dir, s) = store();
    let mut app = App::new(s, gh.clone(), armed_config());
    let fe = Arc::new(FakeExecutor::default());
    if armed {
        app.executor = Some(fe.clone());
    }
    if let Some(f) = approval {
        app.approval = Some(f);
    }
    Consolidated {
        _dir: dir,
        app: Arc::new(app),
        gh,
        fe,
    }
}

fn consolidated_app() -> Consolidated {
    consolidated_with(returns_id(4242), true, None)
}

impl Consolidated {
    fn start_id(&self, i: usize) -> String {
        self.fe.starts.lock().expect("lock")[i].execution_id.clone()
    }

    fn updates(&self) -> Vec<CheckRunUpdate> {
        check_updates(&self.gh)
    }

    async fn claim(&self, pr: i64, stacks: &[&str]) {
        self.app
            .handle_claim(
                "nonprod",
                AcquireClaim {
                    pr,
                    stacks: stacks.iter().map(|s| (*s).to_string()).collect(),
                    now: self.app.now(),
                }
                .into(),
            )
            .await
            .expect("claim");
    }

    async fn pr_sync(&self, pr: i64, sha: &str) {
        webhook_req(
            &self.app,
            WH_SECRET,
            "pull_request",
            &pr_sync_payload(pr, sha),
        )
        .await;
    }

    async fn seed_plan(&self, id: &str, sha: &str, stack: StackState) {
        seed_init(
            &self.app,
            Init {
                id: id.into(),
                repo: "o/r".into(),
                sha: sha.into(),
                pr: 7,
                environment: "nonprod".into(),
                context: "plan/nonprod".into(),
                stacks: vec![stack],
                ..Init::default()
            },
        )
        .await;
    }

    /// SetReport then a RunnerFinalize through the shell.
    async fn finalize_via_shell(&self, id: &str) {
        self.app.store().set_report(id, "report").expect("report");
        self.app
            .handle(7, "nonprod", "o/r", RunnerFinalize::default().into())
            .await
            .expect("handle");
    }

    fn lock_record(&self, sha: &str) -> Option<ApplyLockCheck> {
        self.app
            .store()
            .get_apply_lock_check("nonprod", sha)
            .expect("get apply lock check")
    }
}

#[tokio::test]
async fn consolidated_check_holds_on_overlapping_apply() {
    let c = consolidated_app();
    c.claim(3, &["stacks/a"]).await;
    c.pr_sync(7, "sha-one").await;
    let id = c.start_id(0);
    c.seed_plan(&id, "sha-one", st("stacks/a", Status::Safe))
        .await;
    c.finalize_via_shell(&id).await;
    let ups = c.updates();
    let l = last(&ups);
    assert_eq!(l.conclusion, "", "lock held stays in progress");
    assert_eq!(l.title, "waiting on PR #3's apply");
    let rec = c.lock_record("sha-one").expect("no applylock record");
    assert_eq!(
        (
            rec.state.as_str(),
            rec.kind.as_str(),
            rec.execution_id.as_str(),
            rec.check_run_id
        ),
        ("held", "pr_head", id.as_str(), 4242),
        "{rec:?}"
    );
}

#[tokio::test]
async fn consolidated_check_clear_concludes_success() {
    let c = consolidated_app();
    c.pr_sync(7, "sha-one").await;
    let id = c.start_id(0);
    c.seed_plan(&id, "sha-one", st("stacks/a", Status::Safe))
        .await;
    c.finalize_via_shell(&id).await;
    let ups = c.updates();
    let l = last(&ups);
    assert_eq!(l.conclusion, "success");
    assert!(!l.text.contains("Merge lock"), "{:?}", l.text);
    let rec = c.lock_record("sha-one");
    assert_eq!(rec.map(|r| r.state).as_deref(), Some("clear"));
}

#[tokio::test]
async fn consolidated_release_flips_held_to_success() {
    let c = consolidated_app();
    c.claim(3, &["stacks/a"]).await;
    c.pr_sync(7, "sha-one").await;
    let id = c.start_id(0);
    c.seed_plan(&id, "sha-one", st("stacks/a", Status::Safe))
        .await;
    post_ok(
        &c.app,
        "/api/finalize",
        json!({"id": id, "report_markdown": "report"}),
    )
    .await;
    let ups = c.updates();
    let l = last(&ups);
    assert!(
        l.conclusion.is_empty() && l.title == "waiting on PR #3's apply",
        "precondition: last update after finalize = {l:?}"
    );

    c.app.release_apply_claims("nonprod", 3).await;

    let ups = c.updates();
    let l = last(&ups);
    assert_eq!(l.conclusion, "success");
    assert!(!l.title.starts_with("apply-lock:"), "{:?}", l.title);
    assert!(l.text.contains("report"), "{:?}", l.text);
    let rec = c.lock_record("sha-one");
    assert_eq!(rec.map(|r| r.state).as_deref(), Some("clear"));
}

#[tokio::test]
async fn armed_tier_posts_no_separate_apply_lock_check() {
    let mut gh = returns_id(4242);
    gh.pr_head_sha_fn = Some(Box::new(|_| Ok("sha-one".into())));
    let c = consolidated_with(gh, true, None);
    seed_init(
        &c.app,
        Init {
            id: "seed-1".into(),
            repo: "o/r".into(),
            sha: "sha-zero".into(),
            pr: 7,
            environment: "nonprod".into(),
            context: "plan/nonprod".into(),
            stacks: vec![st("stacks/a", Status::Safe)],
            ..Init::default()
        },
    )
    .await;
    c.pr_sync(7, "sha-one").await;
    let created = created_check_runs(&c.gh);
    assert!(
        !created.iter().any(|n| n.starts_with("apply-lock/")),
        "armed tier created an apply-lock check: {created:?}"
    );
    assert_eq!(created.len(), 1, "{created:?}");
}

#[tokio::test]
async fn merge_group_check_name_follows_arming() {
    for (armed, want) in [(true, "terraform/nonprod"), (false, "apply-lock/nonprod")] {
        let mut gh = returns_id(4242);
        gh.merge_group_prs_fn = Some(Box::new(|_| Ok(vec![5])));
        let c = consolidated_with(gh, armed, None);
        seed_init(
            &c.app,
            Init {
                id: "seed-5".into(),
                repo: "o/r".into(),
                sha: "sha-five".into(),
                pr: 5,
                environment: "nonprod".into(),
                context: "plan/nonprod".into(),
                stacks: vec![st("stacks/a", Status::Safe)],
                ..Init::default()
            },
        )
        .await;
        c.app
            .handle_merge_group("o/r", "mg-sha", "", "checks_requested")
            .await
            .expect("merge group");
        assert_eq!(created_check_runs(&c.gh), [want], "armed={armed}");
    }
}

#[tokio::test]
async fn consolidated_gate_lock_then_release_succeeds() {
    let fake = Arc::new(Fake::with_pool(vec![
        "sa0@project.iam.gserviceaccount.com".into(),
    ]));
    let c = consolidated_with(returns_id(4242), true, Some(Arc::clone(&fake)));
    c.claim(3, &["stacks/a"]).await;
    c.pr_sync(7, "sha-one").await;
    let id = c.start_id(0);
    c.seed_plan(
        &id,
        "sha-one",
        StackState {
            project: "proj-a".into(),
            ..st("stacks/a", Status::Planned)
        },
    )
    .await;
    post_ok(
        &c.app,
        "/api/finalize",
        json!({"id": id, "report_markdown": "report",
               "gates": [{"class": "iam", "target": "proj-a"}]}),
    )
    .await;
    let ups = c.updates();
    let l = last(&ups);
    assert_eq!(l.conclusion, "", "gate pending stays in progress");
    assert_eq!(l.title, "awaiting approval — 0 of 1 gates active");

    fake.approve(&Request {
        class: "iam".into(),
        target: "proj-a".into(),
        pr: 7,
        environment: "nonprod".into(),
        ..Request::default()
    });
    c.app.tick(7, "nonprod").await.expect("reconcile gate");
    let ups = c.updates();
    let l = last(&ups);
    assert_eq!(l.conclusion, "", "lock still held stays in progress");
    assert_eq!(l.title, "waiting on PR #3's apply");

    c.app.release_apply_claims("nonprod", 3).await;
    let ups = c.updates();
    assert_eq!(last(&ups).conclusion, "success");
}

#[tokio::test]
async fn superseded_held_record_does_not_resurrect_old_check() {
    let c = consolidated_app();
    c.claim(3, &["stacks/a"]).await;
    c.pr_sync(7, "sha-one").await;
    let id1 = c.start_id(0);
    c.seed_plan(&id1, "sha-one", st("stacks/a", Status::Safe))
        .await;
    c.finalize_via_shell(&id1).await;
    assert_eq!(
        c.lock_record("sha-one").map(|r| r.state).as_deref(),
        Some("held"),
        "precondition: sha-one held"
    );

    c.pr_sync(7, "sha-two").await;
    let id2 = c.start_id(1);
    c.seed_plan(&id2, "sha-two", st("stacks/a", Status::Safe))
        .await;
    c.finalize_via_shell(&id2).await;
    assert_eq!(
        c.lock_record("sha-two").map(|r| r.state).as_deref(),
        Some("held"),
        "precondition: sha-two held"
    );

    c.app.release_apply_claims("nonprod", 3).await;
    let rec = c.lock_record("sha-two").expect("sha-two record");
    assert_eq!(rec.state, "clear");
}

#[tokio::test]
async fn reevaluate_held_falls_back_to_legacy_when_disarmed() {
    let c = consolidated_with(MockGitHub::default(), false, None);
    c.claim(3, &["stacks/a"]).await;
    seed_init(
        &c.app,
        Init {
            id: "exec-7".into(),
            repo: "o/r".into(),
            sha: "sha-one".into(),
            pr: 7,
            environment: "nonprod".into(),
            context: "plan/nonprod".into(),
            stacks: vec![st("stacks/a", Status::Safe)],
            ..Init::default()
        },
    )
    .await;
    let s = c.app.store();
    s.set_check_run_id("exec-7", 4242).expect("check run id");
    s.set_report("exec-7", "report").expect("report");
    s.upsert_apply_lock_check(&ApplyLockCheck {
        environment: "nonprod".into(),
        head_sha: "sha-one".into(),
        check_run_id: 4242,
        pr: 7,
        repo: "o/r".into(),
        stacks: Some(vec!["stacks/a".into()]),
        state: "held".into(),
        kind: "pr_head".into(),
        execution_id: "exec-7".into(),
    })
    .expect("upsert");

    c.app.reevaluate_held("nonprod").await;

    let ups = c.updates();
    assert_ne!(
        last(&ups).conclusion,
        "success",
        "the lock is still held (merge-lock bypass)"
    );
    let rec = c.lock_record("sha-one").expect("applylock record");
    assert_eq!(rec.state, "held");
}

#[tokio::test]
async fn consolidated_clear_record_survives_failed_patch() {
    let fail_next = Arc::new(AtomicBool::new(false));
    let f = Arc::clone(&fail_next);
    let mut gh = returns_id(4242);
    gh.update_check_run_fn = Some(Box::new(move |_| {
        if f.swap(false, Ordering::SeqCst) {
            Err(tfstackplan_server_clients::Error::msg(
                "transient github error",
            ))
        } else {
            Ok(())
        }
    }));
    let c = consolidated_with(gh, true, None);
    c.claim(3, &["stacks/a"]).await;
    c.pr_sync(7, "sha-one").await;
    let id = c.start_id(0);
    c.seed_plan(&id, "sha-one", st("stacks/a", Status::Safe))
        .await;
    post_ok(
        &c.app,
        "/api/finalize",
        json!({"id": id, "report_markdown": "report"}),
    )
    .await;
    assert_eq!(
        c.lock_record("sha-one").map(|r| r.state).as_deref(),
        Some("held"),
        "precondition"
    );

    fail_next.store(true, Ordering::SeqCst);
    let before = c.updates().len();
    c.app.release_apply_claims("nonprod", 3).await;
    assert_eq!(
        c.lock_record("sha-one").map(|r| r.state).as_deref(),
        Some("held"),
        "a failed patch must not clear the record"
    );
    assert!(
        c.updates().len() > before,
        "the failing patch was attempted"
    );

    c.app.release_apply_claims("nonprod", 3).await;
    let ups = c.updates();
    assert_eq!(last(&ups).conclusion, "success");
    assert_eq!(
        c.lock_record("sha-one").map(|r| r.state).as_deref(),
        Some("clear")
    );
}
