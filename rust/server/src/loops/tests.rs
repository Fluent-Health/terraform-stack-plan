// Ported from internal/server: orphan_sweep_test.go, and the watchdog cases of
// run_trigger_test.go (TestWatchdogFailsVanishedBuild,
// TestWatchdogIgnoresRunnerCreatedExecutions).
use std::sync::{Arc, Mutex};
use std::time::Duration;

use tfstackplan_gate::approval::Request;
use tfstackplan_gate::events::{Finalize, GateTarget, Init, StackState, Status};
use tfstackplan_server_clients::approval::Fake;
use tfstackplan_server_clients::executor::{Backend, Phase, Ref, RunRequest};
use tfstackplan_server_clients::github::MockGitHub;

use crate::testutil::{
    WH_SECRET, post_json, pr_sync_payload, run_trigger_app, run_trigger_gh, seed_projection_target,
    webhook_req,
};
use crate::{App, Config};

// --- orphan_sweep_test.go ---

fn gate_target_state(a: &App) -> String {
    let ts = a.store().targets_for(7, "staging").expect("targets");
    assert_eq!(ts.len(), 1, "targets: {ts:?}");
    ts[0].state.clone()
}

#[tokio::test]
async fn sweep_skips_pr_without_execution_repo() {
    let called = Arc::new(Mutex::new(false));
    let mut gh = MockGitHub::default();
    let c = Arc::clone(&called);
    gh.pr_abandoned_fn = Some(Box::new(move |_| {
        *c.lock().expect("lock") = true;
        Ok(true)
    }));
    let (_d, s) = crate::testutil::store();
    let mut a = App::new(s, Arc::new(gh), Config::default());
    a.approval = Some(Arc::new(Fake::new()));
    let a = Arc::new(a);
    // A gate_targets row with no execution row: OpenGrantPRs yields repo "".
    seed_projection_target(a.store(), 7, "staging", "iam", "p1", "g1", "ACTIVE", "");
    a.sweep_orphaned_grants().await;
    assert!(
        !*called.lock().expect("lock"),
        "sweep must skip a PR with no execution repo"
    );
    assert_eq!(gate_target_state(&a), "ACTIVE");
}

/// Go's `setupActiveGate`: PR 7 / staging driven to a stored ACTIVE grant through
/// init → finalize → approve → tick, with `pr_abandoned` answering `abandoned`.
async fn setup_active_gate(
    abandoned: tfstackplan_server_clients::Result<bool>,
) -> (tempfile::TempDir, Arc<App>) {
    let fake = Arc::new(Fake::with_pool(vec!["sa0".into()]));
    let mut gh = MockGitHub::default();
    gh.create_check_run_fn = Some(Box::new(|_| Ok(1)));
    gh.update_check_run_fn = Some(Box::new(|_| Ok(())));
    gh.pr_abandoned_fn = Some(Box::new(move |_| abandoned.clone()));
    let (d, s) = crate::testutil::store();
    let mut a = App::new(s, Arc::new(gh), Config::default());
    a.approval = Some(fake.clone());
    let a = Arc::new(a);
    post_json(
        &a,
        "/api/init",
        &Init {
            id: "e1".into(),
            repo: "o/r".into(),
            sha: "sha".into(),
            pr: 7,
            environment: "staging".into(),
            stacks: vec![StackState {
                path: "stacks/a".into(),
                project: "proj-a".into(),
                status: Status::Planned,
                ..StackState::default()
            }],
            ..Init::default()
        },
    )
    .await;
    post_json(
        &a,
        "/api/finalize",
        &Finalize {
            id: "e1".into(),
            report_markdown: "# r".into(),
            gates: vec![GateTarget {
                class: "iam".into(),
                target: "proj-a".into(),
            }],
            ..Finalize::default()
        },
    )
    .await;
    fake.approve(&Request {
        class: "iam".into(),
        target: "proj-a".into(),
        pr: 7,
        environment: "staging".into(),
        requester: String::new(),
    });
    a.tick(7, "staging").await.expect("tick");
    assert_eq!(gate_target_state(&a), "ACTIVE", "setup");
    (d, a)
}

#[tokio::test]
async fn sweep_revokes_abandoned_pr_grant() {
    let (_d, a) = setup_active_gate(Ok(true)).await;
    a.sweep_orphaned_grants().await;
    assert_eq!(gate_target_state(&a), "REVOKED");
}

#[tokio::test]
async fn sweep_keeps_open_pr_grant() {
    let (_d, a) = setup_active_gate(Ok(false)).await;
    a.sweep_orphaned_grants().await;
    assert_eq!(gate_target_state(&a), "ACTIVE");
}

#[tokio::test]
async fn sweep_keeps_grant_on_pr_abandoned_error() {
    let (_d, a) =
        setup_active_gate(Err(tfstackplan_server_clients::Error::msg("github down"))).await;
    a.sweep_orphaned_grants().await;
    assert_eq!(gate_target_state(&a), "ACTIVE");
}

#[tokio::test]
async fn sweep_keeps_merged_pr_grant() {
    // A merged PR is not abandoned: its post-merge apply needs the grant.
    let (_d, a) = setup_active_gate(Ok(false)).await;
    a.sweep_orphaned_grants().await;
    assert_eq!(gate_target_state(&a), "ACTIVE");
}

// --- run_trigger_test.go: the start watchdog ---

/// Go's `fakeExecutor` with a probe phase the test can change mid-run.
#[derive(Default)]
struct PhaseExecutor {
    starts: Mutex<Vec<RunRequest>>,
    phase: Mutex<Option<Phase>>,
}

#[async_trait::async_trait]
impl Backend for PhaseExecutor {
    async fn start(&self, req: &RunRequest) -> tfstackplan_server_clients::Result<Ref> {
        self.starts.lock().expect("lock").push(req.clone());
        Ok(Ref {
            id: format!("build-{}", req.execution_id),
        })
    }

    async fn cancel(&self, _r: &Ref) -> tfstackplan_server_clients::Result<()> {
        Ok(())
    }

    async fn probe(&self, _r: &Ref) -> tfstackplan_server_clients::Result<Phase> {
        Ok(self.phase.lock().expect("lock").unwrap_or(Phase::Working))
    }
}

fn age_an_hour(a: &App, id: &str) {
    a.store()
        .exec(|c| {
            c.execute(
                "UPDATE executions SET created_at = datetime('now', '-1 hour') WHERE id = ?1",
                [id],
            )
        })
        .expect("age row");
}

fn status_of(a: &App, id: &str) -> String {
    a.store()
        .get_execution(id)
        .expect("get")
        .expect("row")
        .status
}

#[tokio::test]
async fn watchdog_fails_vanished_build() {
    let fe = Arc::new(PhaseExecutor::default());
    let (_d, a) = run_trigger_app(run_trigger_gh(), fe.clone());
    webhook_req(
        &a,
        WH_SECRET,
        "pull_request",
        &pr_sync_payload(7, "sha-one"),
    )
    .await;
    let id = {
        let starts = fe.starts.lock().expect("lock");
        assert_eq!(starts.len(), 1, "starts = {starts:?}");
        starts[0].execution_id.clone()
    };
    age_an_hour(&a, &id);

    *fe.phase.lock().expect("lock") = Some(Phase::Working);
    a.watch_runs_once(Duration::from_secs(600)).await;
    assert_eq!(
        status_of(&a, &id),
        "in_progress",
        "working build left alone"
    );

    *fe.phase.lock().expect("lock") = Some(Phase::NotFound);
    a.watch_runs_once(Duration::from_secs(600)).await;
    assert_eq!(
        status_of(&a, &id),
        "failure",
        "vanished build fails the run"
    );
}

#[tokio::test]
async fn watchdog_ignores_runner_created_executions() {
    let (_d, a) = run_trigger_app(run_trigger_gh(), Arc::new(PhaseExecutor::default()));
    crate::testutil::seed_init(
        &a,
        Init {
            id: "runner-e1".into(),
            pr: 9,
            environment: "nonprod".into(),
            repo: "o/r".into(),
            ..Init::default()
        },
    )
    .await;
    age_an_hour(&a, "runner-e1");
    a.watch_runs_once(Duration::from_secs(600)).await;
    assert_eq!(status_of(&a, "runner-e1"), "in_progress");
}
