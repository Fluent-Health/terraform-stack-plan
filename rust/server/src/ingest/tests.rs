// Ported from internal/server: gate_test.go, gate_check_test.go, gate_e2e_test.go,
// gate_failclosed_test.go, apply_lost_tick_test.go and handlers_test.go (its
// supersede and revive cases are in shell/tests.rs, beside supersede_execution).
//
// Go's `reconcileGate(pr, env)` is `App::tick`; `post`/`postResp` go through the
// router with no API verifier, as Go's httptest server does.
use std::collections::BTreeMap;
use std::sync::Arc;
use std::sync::atomic::{AtomicBool, Ordering};
use std::time::Duration;

use async_trait::async_trait;
use tfstackplan_gate::approval::{Grant, Request};
use tfstackplan_gate::events::{
    Category, Claim, Counts, Finalize, GateCheck, GateRevoke, GateTarget, Init, Phase, PhaseEvent,
    StackState, Status, Update,
};
use tfstackplan_gate::reconcile::RunnerFinalize;
use tfstackplan_server_clients::approval::{Backend, Fake};
use tfstackplan_server_clients::github::{CheckRunUpdate, MockGitHub};
use tfstackplan_store::ClaimRow;

use crate::testutil::{
    FakeExecutor, Wire, check_updates, post_json, run_trigger_app, run_trigger_gh, seed_init, send,
};
use crate::{App, Config};

/// An app over `gh` with an optional approval backend.
fn app_of(
    gh: MockGitHub,
    approval: Option<Arc<dyn Backend>>,
) -> (tempfile::TempDir, Arc<App>, Arc<MockGitHub>) {
    let (d, s) = crate::testutil::store();
    let gh = Arc::new(gh);
    let mut a = App::new(s, gh.clone(), Config::default());
    a.approval = approval;
    (d, Arc::new(a), gh)
}

/// The MockGitHub most Go gate tests use: check run 1, updates accepted.
fn gh1() -> MockGitHub {
    let mut gh = MockGitHub::default();
    gh.create_check_run_fn = Some(Box::new(|_| Ok(1)));
    gh.update_check_run_fn = Some(Box::new(|_| Ok(())));
    gh
}

fn gh_check_run(id: i64) -> MockGitHub {
    let mut gh = MockGitHub::default();
    gh.create_check_run_fn = Some(Box::new(move |_| Ok(id)));
    gh
}

fn fake_with_pool(pool: &[&str]) -> Arc<Fake> {
    Arc::new(Fake::with_pool(
        pool.iter().map(|p| (*p).to_string()).collect(),
    ))
}

fn iam(target: &str) -> GateTarget {
    GateTarget {
        class: "iam".into(),
        target: target.into(),
    }
}

fn req(target: &str, pr: i64, env: &str) -> Request {
    Request {
        class: "iam".into(),
        target: target.into(),
        pr,
        environment: env.into(),
        requester: String::new(),
    }
}

fn stack(path: &str, project: &str, status: Status) -> StackState {
    StackState {
        path: path.into(),
        project: project.into(),
        status,
        ..StackState::default()
    }
}

fn exec_init(id: &str, pr: i64, env: &str, context: &str, stacks: Vec<StackState>) -> Init {
    Init {
        id: id.into(),
        repo: "o/r".into(),
        sha: "sha".into(),
        pr,
        environment: env.into(),
        context: context.into(),
        stacks,
        ..Init::default()
    }
}

fn fin(id: &str, md: &str, gates: Vec<GateTarget>) -> Finalize {
    Finalize {
        id: id.into(),
        report_markdown: md.into(),
        gates,
        ..Finalize::default()
    }
}

async fn gate_check(a: &Arc<App>, pr: i64, env: &str) -> Wire {
    let body = serde_json::to_string(&GateCheck {
        pr,
        environment: env.into(),
    })
    .expect("marshal");
    send(a, "POST", "/api/gate/check", "", &body).await
}

fn requester_of(w: &Wire) -> String {
    let body: BTreeMap<String, String> = serde_json::from_slice(&w.body).expect("decode body");
    body.get("requester").cloned().unwrap_or_default()
}

/// Go's `newGatedExecution`: a gated plan (one stack on proj-a) finalized with an
/// iam gate on proj-a.
async fn new_gated_execution(a: &Arc<App>) {
    post_json(
        a,
        "/api/init",
        &exec_init(
            "e1",
            7,
            "staging",
            "",
            vec![stack("stacks/a", "proj-a", Status::Planned)],
        ),
    )
    .await;
    post_json(
        a,
        "/api/finalize",
        &fin("e1", "# report", vec![iam("proj-a")]),
    )
    .await;
}

/// The last non-empty conclusion among updates not on the apply-lock check.
fn last_conclusion(gh: &MockGitHub) -> String {
    check_updates(gh)
        .into_iter()
        .rfind(|u| !u.title.starts_with("apply-lock") && !u.conclusion.is_empty())
        .map(|u| u.conclusion)
        .unwrap_or_default()
}

/// The last update not on the apply-lock check.
fn last_gate_update(gh: &MockGitHub) -> CheckRunUpdate {
    check_updates(gh)
        .into_iter()
        .rfind(|u| !u.title.starts_with("apply-lock"))
        .unwrap_or_default()
}

// --- gate_test.go ---

#[tokio::test]
async fn finalize_requests_grants() {
    let fake = Arc::new(Fake::new());
    let (_d, a, _gh) = app_of(MockGitHub::default(), Some(fake.clone()));
    new_gated_execution(&a).await;
    let ts = a.store().targets_for(7, "staging").expect("targets");
    assert_eq!(ts.len(), 1, "targets = {ts:?}");
    assert_eq!(ts[0].target, "proj-a");
    assert_eq!(ts[0].state, "AWAITING");
    assert!(!ts[0].grant_name.is_empty());
    assert_eq!(
        fake.list_grants("iam", "proj-a").await.expect("list").len(),
        1
    );
}

#[tokio::test]
async fn reconcile_gate_flips_active_on_approval() {
    let fake = Arc::new(Fake::new());
    let (_d, a, gh) = app_of(gh1(), Some(fake.clone()));
    new_gated_execution(&a).await;
    let g = a.store().load_graph("e1").expect("graph");
    assert_eq!(g.stacks[0].status, Status::Gated);
    fake.approve(&req("proj-a", 7, "staging"));
    let _ = a.tick(7, "staging").await;
    let ts = a.store().targets_for(7, "staging").expect("targets");
    assert_eq!(ts[0].state, "ACTIVE");
    let g = a.store().load_graph("e1").expect("graph");
    assert_eq!(g.stacks[0].status, Status::Safe);
    let concl = check_updates(&gh)
        .into_iter()
        .rfind(|u| !u.conclusion.is_empty())
        .map(|u| u.conclusion)
        .unwrap_or_default();
    assert_eq!(concl, "success");
}

#[tokio::test]
async fn latest_execution_id() {
    let (_d, a, _gh) = app_of(MockGitHub::default(), None);
    seed_init(
        &a,
        Init {
            id: "e1".into(),
            repo: "o/r".into(),
            pr: 7,
            environment: "staging".into(),
            ..Init::default()
        },
    )
    .await;
    assert_eq!(
        a.store().latest_execution_id(7, "staging").expect("latest"),
        Some("e1".to_string())
    );
    assert_eq!(
        a.store().latest_execution_id(99, "nope").expect("latest"),
        None
    );
}

#[tokio::test]
async fn gate_check_fail_closed() {
    let fake = Arc::new(Fake::new());
    let (_d, a, _gh) = app_of(MockGitHub::default(), Some(fake.clone()));
    assert_eq!(gate_check(&a, 7, "staging").await.status, 409, "unplanned");
    new_gated_execution(&a).await;
    assert_eq!(
        gate_check(&a, 7, "staging").await.status,
        409,
        "before approval"
    );
    fake.approve(&req("proj-a", 7, "staging"));
    assert_eq!(gate_check(&a, 7, "staging").await.status, 200, "approved");
}

#[tokio::test]
async fn gate_check_clean_plan_passes() {
    let (_d, a, _gh) = app_of(MockGitHub::default(), Some(Arc::new(Fake::new())));
    post_json(
        &a,
        "/api/init",
        &Init {
            id: "e1".into(),
            repo: "o/r".into(),
            pr: 8,
            environment: "staging".into(),
            stacks: vec![stack("a", "", Status::Planned)],
            ..Init::default()
        },
    )
    .await;
    post_json(&a, "/api/finalize", &fin("e1", "# report", vec![])).await;
    assert_eq!(gate_check(&a, 8, "staging").await.status, 200);
}

#[tokio::test]
async fn gate_revoke() {
    let fake = Arc::new(Fake::new());
    let (_d, a, _gh) = app_of(MockGitHub::default(), Some(fake.clone()));
    new_gated_execution(&a).await;
    fake.approve(&req("proj-a", 7, "staging"));
    let code = post_json(
        &a,
        "/api/gate/revoke",
        &GateRevoke {
            pr: 7,
            environment: "staging".into(),
        },
    )
    .await;
    assert_eq!(code, 200);
    let grants = fake.list_grants("iam", "proj-a").await.expect("list");
    assert_eq!(
        grants[0].state,
        tfstackplan_gate::approval::GrantState::Revoked
    );
}

#[tokio::test]
async fn gate_check_returns_requester() {
    let fake = fake_with_pool(&["sa0@project.iam.gserviceaccount.com"]);
    let (_d, a, _gh) = app_of(MockGitHub::default(), Some(fake.clone()));
    new_gated_execution(&a).await;
    fake.approve(&req("proj-a", 7, "staging"));
    let w = gate_check(&a, 7, "staging").await;
    assert_eq!(w.status, 200);
    assert_eq!(requester_of(&w), "sa0@project.iam.gserviceaccount.com");
}

#[tokio::test]
async fn apply_finalize_keeps_plan_established_gate() {
    let fake = fake_with_pool(&["tf-applier-0@x"]);
    let (_d, a, _gh) = app_of(gh1(), Some(fake.clone()));
    post_json(
        &a,
        "/api/init",
        &exec_init(
            "plan-1",
            42,
            "prod",
            "",
            vec![stack("stacks/a", "proj-a", Status::Planned)],
        ),
    )
    .await;
    post_json(&a, "/api/finalize", &fin("plan-1", "", vec![iam("proj-a")])).await;
    fake.approve(&req("proj-a", 42, "prod"));
    let _ = a.tick(42, "prod").await;
    post_json(
        &a,
        "/api/init",
        &exec_init(
            "apply-1",
            42,
            "prod",
            "apply/prod",
            vec![stack("stacks/a", "proj-a", Status::Pending)],
        ),
    )
    .await;
    post_json(&a, "/api/finalize", &fin("apply-1", "", vec![])).await;
    let w = gate_check(&a, 42, "prod").await;
    assert_eq!(w.status, 200);
    assert_eq!(
        requester_of(&w),
        "tf-applier-0@x",
        "apply-time finalize clobbered the plan gate"
    );
}

#[tokio::test]
async fn revoke_orphans_revokes_across_environments() {
    let fake = Arc::new(Fake::new());
    let (_d, a, _gh) = app_of(MockGitHub::default(), Some(fake.clone()));
    for (id, env) in [("e-np", "nonprod"), ("e-pr", "prod")] {
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
    }
    a.handle(
        7,
        "nonprod",
        "o/r",
        RunnerFinalize {
            gates: vec![iam("proj-a")],
            apply_context: false,
        }
        .into(),
    )
    .await
    .expect("finalize nonprod");
    a.handle(
        7,
        "prod",
        "o/r",
        RunnerFinalize {
            gates: vec![iam("proj-b")],
            apply_context: false,
        }
        .into(),
    )
    .await
    .expect("finalize prod");
    a.revoke_orphans(7).await;
    for target in ["proj-a", "proj-b"] {
        for g in fake.list_grants("iam", target).await.expect("list") {
            assert!(
                !(g.request.pr == 7 && g.state.open()),
                "grant PR 7 iam/{target} still open after revokeOrphans"
            );
        }
    }
}

#[tokio::test]
async fn apply_time_reclassify_recovers_stranded_pr() {
    let fake = fake_with_pool(&["sa0@x"]);
    let (_d, a, _gh) = app_of(gh1(), Some(fake.clone()));
    assert_eq!(gate_check(&a, 42, "prod").await.status, 409, "pre-classify");
    post_json(
        &a,
        "/api/init",
        &exec_init(
            "apply-1",
            42,
            "prod",
            "apply/prod",
            vec![stack("stacks/a", "proj-a", Status::Pending)],
        ),
    )
    .await;
    post_json(
        &a,
        "/api/finalize",
        &fin("apply-1", "", vec![iam("proj-a")]),
    )
    .await;
    let ts = a.store().targets_for(42, "prod").expect("targets");
    assert_eq!(ts.len(), 1, "targets after re-classify = {ts:?}");
    assert_eq!(ts[0].target, "proj-a");
    assert!(!ts[0].grant_name.is_empty());
    assert_eq!(gate_check(&a, 42, "prod").await.status, 409, "pre-approval");
    fake.approve(&req("proj-a", 42, "prod"));
    let _ = a.tick(42, "prod").await;
    assert_eq!(gate_check(&a, 42, "prod").await.status, 200, "approved");
}

#[tokio::test]
async fn gate_check_not_classified_carries_code() {
    let (_d, a, _gh) = app_of(MockGitHub::default(), None);
    let body = serde_json::to_vec(&GateCheck {
        pr: 999,
        environment: "nonprod".into(),
    })
    .expect("marshal");
    let resp = super::gate_check(Arc::clone(&a), &body).await;
    assert_eq!(resp.status().as_u16(), 409);
    let bytes = axum::body::to_bytes(resp.into_body(), usize::MAX)
        .await
        .expect("body");
    let e: serde_json::Value = serde_json::from_slice(&bytes).expect("body not JSON");
    assert_eq!(e["code"], "GATE-001");
}

// --- gate_check_test.go ---

#[tokio::test]
async fn gate_check_never_planned_fails_closed() {
    let (_d, a, _gh) = app_of(MockGitHub::default(), Some(Arc::new(Fake::new())));
    assert_eq!(gate_check(&a, 7, "nonprod").await.status, 409);
}

#[tokio::test]
async fn gate_check_clean_planned_passes() {
    let (_d, a, _gh) = app_of(MockGitHub::default(), Some(Arc::new(Fake::new())));
    a.handle(7, "nonprod", "repo", RunnerFinalize::default().into())
        .await
        .expect("finalize");
    assert_eq!(gate_check(&a, 7, "nonprod").await.status, 200);
}

#[tokio::test]
async fn gate_check_gated_not_active_fails() {
    let (_d, a, _gh) = app_of(MockGitHub::default(), Some(Arc::new(Fake::new())));
    a.handle(
        7,
        "nonprod",
        "repo",
        RunnerFinalize {
            gates: vec![iam("proj-a")],
            apply_context: false,
        }
        .into(),
    )
    .await
    .expect("finalize");
    assert_eq!(gate_check(&a, 7, "nonprod").await.status, 409);
}

#[tokio::test]
async fn gate_check_all_active_returns_requester() {
    let fake = fake_with_pool(&["sa0@project.iam.gserviceaccount.com"]);
    let (_d, a, _gh) = app_of(gh1(), Some(fake.clone()));
    a.handle(
        7,
        "nonprod",
        "repo",
        RunnerFinalize {
            gates: vec![iam("proj-a")],
            apply_context: false,
        }
        .into(),
    )
    .await
    .expect("finalize");
    fake.approve(&req("proj-a", 7, "nonprod"));
    a.tick(7, "nonprod").await.expect("reconcile");
    let w = gate_check(&a, 7, "nonprod").await;
    assert_eq!(w.status, 200);
    assert_eq!(requester_of(&w), "sa0@project.iam.gserviceaccount.com");
}

#[tokio::test]
async fn gate_reconcile_nudge_accepts() {
    let (_d, a, _gh) = app_of(MockGitHub::default(), None);
    assert_eq!(
        send(&a, "POST", "/api/gate/reconcile", "", "").await.status,
        202
    );
}

// --- gate_e2e_test.go ---

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn gated_lifecycle_e2e() {
    let fake = fake_with_pool(&["sa0", "sa1"]);
    let (_d, a, gh) = app_of(gh1(), Some(fake.clone()));
    new_gated_execution(&a).await;
    assert_eq!(last_conclusion(&gh), "", "gated plan stays in_progress");
    let stack_a = |a: &App| {
        a.store().load_graph("e1").expect("graph").stacks[0]
            .status
            .as_str()
            .to_string()
    };
    assert_eq!(stack_a(&a), "gated");
    assert_eq!(gate_check(&a, 7, "staging").await.status, 409);

    fake.approve(&req("proj-a", 7, "staging"));
    let loop_task = tokio::spawn(Arc::clone(&a).reconcile_loop(Duration::from_millis(10)));
    let deadline = tokio::time::Instant::now() + Duration::from_secs(2);
    loop {
        let ts = a.store().targets_for(7, "staging").expect("targets");
        if ts.len() == 1 && ts[0].state == "ACTIVE" && last_conclusion(&gh) == "success" {
            break;
        }
        assert!(
            tokio::time::Instant::now() < deadline,
            "gate did not converge to ACTIVE within 2s"
        );
        tokio::time::sleep(Duration::from_millis(10)).await;
    }
    loop_task.abort();

    assert_eq!(gate_check(&a, 7, "staging").await.status, 200);
    assert_eq!(stack_a(&a), "safe");
    let code = post_json(
        &a,
        "/api/gate/revoke",
        &GateRevoke {
            pr: 7,
            environment: "staging".into(),
        },
    )
    .await;
    assert_eq!(code, 200);
}

// --- gate_failclosed_test.go ---

/// Go's `errListGrantsBackend`: a Fake whose ListGrants fails once `fail` is set.
struct ErrListGrants {
    inner: Fake,
    fail: AtomicBool,
}

#[async_trait]
impl Backend for ErrListGrants {
    async fn request_grant(&self, r: &Request) -> tfstackplan_server_clients::Result<Grant> {
        self.inner.request_grant(r).await
    }

    async fn list_grants(
        &self,
        class: &str,
        target: &str,
    ) -> tfstackplan_server_clients::Result<Vec<Grant>> {
        if self.fail.load(Ordering::SeqCst) {
            return Err(tfstackplan_server_clients::Error::msg("pam unreachable"));
        }
        self.inner.list_grants(class, target).await
    }

    async fn revoke(&self, r: &Request) -> tfstackplan_server_clients::Result<()> {
        self.inner.revoke(r).await
    }
}

#[tokio::test]
async fn gate_check_fails_closed_on_reconcile_error() {
    // Go swaps in a fresh failing backend after seeding; here the seeding backend
    // starts failing ListGrants instead.
    let backend = Arc::new(ErrListGrants {
        inner: Fake::new(),
        fail: AtomicBool::new(false),
    });
    let (_d, a, _gh) = app_of(gh1(), Some(backend.clone()));
    seed_init(
        &a,
        Init {
            id: "e1".into(),
            pr: 7,
            environment: "staging".into(),
            repo: "r".into(),
            ..Init::default()
        },
    )
    .await;
    a.handle(
        7,
        "staging",
        "r",
        RunnerFinalize {
            gates: vec![iam("proj-a")],
            apply_context: false,
        }
        .into(),
    )
    .await
    .expect("finalize");
    backend.fail.store(true, Ordering::SeqCst);
    assert_eq!(gate_check(&a, 7, "staging").await.status, 503);
}

// --- apply_lost_tick_test.go ---

const LOST_ID: &str = "run-7-staging-apply-abc123abc123-a1";

/// Go's `lostTickApply`: an apply whose third stack's terminal tick is lost,
/// replayed up to the terminal finalize `f`.
async fn lost_tick_apply(
    f: Finalize,
) -> (
    tfstackplan_gate::events::Graph,
    tfstackplan_store::ExecutionRow,
    String,
) {
    let mut gh = MockGitHub::default();
    gh.create_check_run_fn = Some(Box::new(|_| Ok(42)));
    gh.update_check_run_fn = Some(Box::new(|_| Ok(())));
    let (_d, a, gh) = app_of(gh, None);
    let stacks = ["stacks/a", "stacks/b", "stacks/c"];
    post_json(
        &a,
        "/api/init",
        &Init {
            id: LOST_ID.into(),
            repo: "o/r".into(),
            sha: "abc123abc123".into(),
            pr: 7,
            environment: "staging".into(),
            context: "apply/staging".into(),
            stacks: stacks
                .iter()
                .map(|s| stack(s, "", Status::Pending))
                .collect(),
            ..Init::default()
        },
    )
    .await;
    post_json(&a, "/api/finalize", &fin(LOST_ID, "classify pass", vec![])).await;
    let phase = |p: &str| PhaseEvent {
        id: LOST_ID.into(),
        phase: Phase::new(p),
        ..PhaseEvent::default()
    };
    let update = |s: &str, st: Status| Update {
        id: LOST_ID.into(),
        stack: s.into(),
        status: st,
        detail: String::new(),
    };
    post_json(&a, "/api/phase", &phase(Phase::APPLYING)).await;
    for s in stacks {
        post_json(&a, "/api/update", &update(s, Status::Running)).await;
    }
    let g = a.store().load_graph(LOST_ID).expect("graph");
    assert_eq!(
        g.stacks
            .iter()
            .filter(|s| s.status == Status::Running)
            .count(),
        3,
        "classify finalize concluded stacks early: {:?}",
        g.stacks
    );
    post_json(&a, "/api/update", &update(stacks[0], Status::Safe)).await;
    post_json(&a, "/api/update", &update(stacks[1], Status::Safe)).await;
    post_json(&a, "/api/phase", &phase(Phase::REPORT)).await;
    post_json(&a, "/api/finalize", &f).await;
    let g = a.store().load_graph(LOST_ID).expect("graph");
    let e = a.store().get_execution(LOST_ID).expect("get").expect("row");
    let concl = check_updates(&gh)
        .into_iter()
        .rfind(|u| !u.conclusion.is_empty())
        .map(|u| u.conclusion)
        .unwrap_or_default();
    (g, e, concl)
}

fn count_status(g: &tfstackplan_gate::events::Graph, st: Status) -> usize {
    g.stacks.iter().filter(|s| s.status == st).count()
}

#[tokio::test]
async fn successful_apply_finalize_concludes_lost_tick() {
    let (g, e, concl) = lost_tick_apply(fin(LOST_ID, "", vec![])).await;
    assert_eq!(count_status(&g, Status::Safe), 3, "{:?}", g.stacks);
    assert_eq!(e.status, "success");
    assert_eq!(concl, "success");
}

#[tokio::test]
async fn failed_apply_finalize_does_not_conclude_lost_tick() {
    let (g, e, concl) = lost_tick_apply(Finalize {
        failed: true,
        ..fin(LOST_ID, "", vec![])
    })
    .await;
    assert_eq!(count_status(&g, Status::Safe), 2, "{:?}", g.stacks);
    assert_eq!(count_status(&g, Status::Aborted), 1, "{:?}", g.stacks);
    assert_eq!(e.status, "failure");
    assert_eq!(concl, "failure");
}

// --- handlers_test.go ---

#[tokio::test]
async fn init_creates_execution_and_check_run_once() {
    let (_d, a, gh) = app_of(gh_check_run(555), None);
    let code = post_json(
        &a,
        "/api/phase",
        &PhaseEvent {
            id: "e1".into(),
            repo: "o/r".into(),
            sha: "sha".into(),
            pr: 7,
            environment: "staging".into(),
            phase: Phase::new(Phase::WARMING),
            ..PhaseEvent::default()
        },
    )
    .await;
    assert_eq!(code, 200, "phase");
    let init = Init {
        stacks: vec![stack("a", "", Status::Unset), stack("b", "", Status::Unset)],
        edges: vec![tfstackplan_gate::events::Edge {
            from: "a".into(),
            to: "b".into(),
        }],
        ..exec_init("e1", 7, "staging", "", vec![])
    };
    assert_eq!(post_json(&a, "/api/init", &init).await, 200, "init");
    assert_eq!(
        gh.create_check_run_calls(),
        1,
        "idempotent across phase+init"
    );
    let e = a.store().get_execution("e1").expect("get").expect("row");
    assert_eq!(e.check_run_id, Some(555));
    assert_eq!(e.phase, Phase::WARMING);
}

#[tokio::test]
async fn update_ticks_stack_and_patches() {
    let (_d, a, gh) = app_of(gh1(), None);
    post_json(
        &a,
        "/api/init",
        &exec_init("e1", 7, "staging", "", vec![stack("a", "", Status::Unset)]),
    )
    .await;
    let code = post_json(
        &a,
        "/api/update",
        &Update {
            id: "e1".into(),
            stack: "a".into(),
            status: Status::Running,
            detail: String::new(),
        },
    )
    .await;
    assert_eq!(code, 200);
    let g = a.store().load_graph("e1").expect("graph");
    assert_eq!(g.stacks[0].status, Status::Running);
    let last = check_updates(&gh).last().cloned().unwrap_or_default();
    assert!(
        !last.summary.is_empty(),
        "non-empty check-run summary after update"
    );
}

#[tokio::test]
async fn finalize_clean_plan_concludes_success() {
    let (_d, a, gh) = app_of(gh1(), None);
    post_json(
        &a,
        "/api/init",
        &exec_init(
            "e1",
            7,
            "staging",
            "",
            vec![stack("a", "", Status::Planned)],
        ),
    )
    .await;
    assert_eq!(
        post_json(&a, "/api/finalize", &fin("e1", "# report", vec![])).await,
        200
    );
    assert_eq!(last_gate_update(&gh).conclusion, "success");
}

#[tokio::test]
async fn finalize_gated_plan_stays_pending() {
    let (_d, a, gh) = app_of(gh1(), Some(Arc::new(Fake::new())));
    post_json(
        &a,
        "/api/init",
        &exec_init(
            "e1",
            7,
            "staging",
            "",
            vec![stack("a", "proj-a", Status::Planned)],
        ),
    )
    .await;
    post_json(
        &a,
        "/api/finalize",
        &fin("e1", "# report", vec![iam("proj-a")]),
    )
    .await;
    let u = last_gate_update(&gh);
    assert_eq!(u.conclusion, "", "in_progress");
    assert_eq!(u.title, "awaiting approval — 0 of 1 gates active");
    let g = a.store().load_graph("e1").expect("graph");
    assert_eq!(g.stacks[0].status, Status::Gated);
    let ts = a.store().targets_for(7, "staging").expect("targets");
    assert_eq!(ts.len(), 1);
    assert_eq!(ts[0].state, "AWAITING");
}

#[tokio::test]
async fn finalize_sets_terminal_status_for_plan_and_verify() {
    let (_d, a, _gh) = app_of(MockGitHub::default(), None);
    let mk = |id: &str, ctx: &str, st: Status| Init {
        sha: "s".into(),
        ..exec_init(id, 7, "staging", ctx, vec![stack("a", "", st)])
    };
    let status = |a: &App, id: &str| {
        a.store()
            .get_execution(id)
            .ok()
            .flatten()
            .map(|e| e.status)
            .unwrap_or_default()
    };
    post_json(&a, "/api/init", &mk("p1", "plan/staging", Status::Planned)).await;
    post_json(&a, "/api/finalize", &fin("p1", "# report", vec![])).await;
    assert_eq!(status(&a, "p1"), "success", "plan");
    post_json(
        &a,
        "/api/init",
        &mk("v1", "verify/staging", Status::Pending),
    )
    .await;
    post_json(&a, "/api/finalize", &fin("v1", "✅", vec![])).await;
    assert_eq!(status(&a, "v1"), "success", "verify");
    post_json(
        &a,
        "/api/init",
        &mk("ap1", "apply/staging", Status::Pending),
    )
    .await;
    post_json(&a, "/api/finalize", &fin("ap1", "classify pass", vec![])).await;
    assert_ne!(
        status(&a, "ap1"),
        "success",
        "mid-apply finalize is not terminal"
    );
}

fn bare_init(id: &str) -> Init {
    Init {
        id: id.into(),
        repo: "o/r".into(),
        environment: "staging".into(),
        stacks: vec![stack("stacks/a", "", Status::Unset)],
        ..Init::default()
    }
}

#[tokio::test]
async fn finalize_stores_per_stack_plan() {
    let (_d, a, _gh) = app_of(MockGitHub::default(), None);
    post_json(&a, "/api/init", &bare_init("e1")).await;
    post_json(
        &a,
        "/api/finalize",
        &Finalize {
            stack_reports: BTreeMap::from([("stacks/a".into(), "PLAN_A_SECTION".into())]),
            ..fin("e1", "combined", vec![])
        },
    )
    .await;
    let out = a
        .store()
        .get_stack_output("e1", "stacks/a", "plan")
        .expect("get");
    assert_eq!(
        out.map(|(_, excerpt)| excerpt),
        Some("PLAN_A_SECTION".to_string())
    );
}

#[tokio::test]
async fn finalize_stores_categories() {
    let (_d, a, _gh) = app_of(MockGitHub::default(), None);
    post_json(&a, "/api/init", &bare_init("e1")).await;
    let cat = |n: &str, i: &str| Category {
        name: n.into(),
        icon: i.into(),
        ..Category::default()
    };
    post_json(
        &a,
        "/api/finalize",
        &Finalize {
            categories: BTreeMap::from([(
                "stacks/a".into(),
                vec![cat("iam", "🔐"), cat("destructive", "💣")],
            )]),
            ..fin("e1", "r", vec![])
        },
    )
    .await;
    let g = a.store().load_graph("e1").expect("graph");
    assert_eq!(g.stacks.len(), 1);
    assert_eq!(g.stacks[0].categories.len(), 2);
    assert_eq!(g.stacks[0].categories[0].name, "iam");
}

#[tokio::test]
async fn finalize_backfills_counts() {
    let (_d, a, _gh) = app_of(MockGitHub::default(), None);
    post_json(&a, "/api/init", &bare_init("e1")).await;
    post_json(
        &a,
        "/api/finalize",
        &Finalize {
            counts: BTreeMap::from([(
                "stacks/a".into(),
                Counts {
                    add: 6,
                    ..Counts::default()
                },
            )]),
            ..fin("e1", "r", vec![])
        },
    )
    .await;
    let g = a.store().load_graph("e1").expect("graph");
    assert_eq!(g.stacks.len(), 1);
    assert_eq!(g.stacks[0].counts.map(|c| c.add), Some(6));
}

fn statuses(a: &App, id: &str) -> BTreeMap<String, Status> {
    a.store()
        .load_graph(id)
        .expect("graph")
        .stacks
        .into_iter()
        .map(|s| (s.path, s.status))
        .collect()
}

fn failed(id: &str) -> Finalize {
    Finalize {
        failed: true,
        ..fin(id, "", vec![])
    }
}

#[tokio::test]
async fn finalize_failed_marks_running_stacks_aborted() {
    let (_d, a, _gh) = app_of(gh_check_run(1), None);
    post_json(
        &a,
        "/api/init",
        &exec_init(
            "e1",
            7,
            "staging",
            "",
            vec![
                stack("a", "", Status::Running),
                stack("b", "", Status::Planned),
            ],
        ),
    )
    .await;
    post_json(
        &a,
        "/api/finalize",
        &Finalize {
            report_markdown: "# report".into(),
            ..failed("e1")
        },
    )
    .await;
    let got = statuses(&a, "e1");
    assert_eq!((got["a"], got["b"]), (Status::Aborted, Status::Planned));
}

#[tokio::test]
async fn finalize_failed_marks_non_terminal_aborted() {
    let (_d, a, _gh) = app_of(gh_check_run(1), None);
    post_json(
        &a,
        "/api/init",
        &exec_init(
            "exec-abort",
            9,
            "staging",
            "",
            vec![
                stack("a", "", Status::Running),
                stack("b", "", Status::Running),
            ],
        ),
    )
    .await;
    post_json(
        &a,
        "/api/update",
        &Update {
            id: "exec-abort".into(),
            stack: "a".into(),
            status: Status::Failed,
            detail: "boom".into(),
        },
    )
    .await;
    post_json(&a, "/api/finalize", &failed("exec-abort")).await;
    let got = statuses(&a, "exec-abort");
    assert_eq!(got["a"], Status::Failed, "a failed on its own");
    assert_eq!(got["b"], Status::Aborted, "b never finished");
    let e = a
        .store()
        .get_execution("exec-abort")
        .expect("get")
        .expect("row");
    assert_eq!(e.status, "failure");
}

fn seed_claim(a: &App) {
    let expires = jiff::Timestamp::now() + jiff::SignedDuration::from_hours(1);
    let expires = expires.round(jiff::Unit::Second).expect("truncate");
    a.store()
        .replace_claims(
            "prod",
            &[(
                "stacks/core".to_string(),
                ClaimRow {
                    owner_pr: 42,
                    expires_at: expires,
                },
            )],
        )
        .expect("seed claim");
}

#[tokio::test]
async fn claims_list_wire_shape() {
    let (_d, a, _gh) = app_of(MockGitHub::default(), None);
    seed_claim(&a);
    let w = send(
        &a,
        "POST",
        "/api/claims/list",
        "",
        r#"{"environment":"prod"}"#,
    )
    .await;
    assert_eq!(w.status, 200);
    let claims: Vec<Claim> = serde_json::from_slice(&w.body)
        .unwrap_or_else(|e| panic!("decode into []events.Claim: {e} (body: {})", w.text()));
    assert_eq!(claims.len(), 1, "body: {}", w.text());
    let c = &claims[0];
    assert_eq!(
        (c.environment.as_str(), c.stack_path.as_str(), c.owner_pr),
        ("prod", "stacks/core", 42),
        "body: {}",
        w.text()
    );
}

#[tokio::test]
async fn update_rejects_unknown_status() {
    let (_d, a, _gh) = app_of(MockGitHub::default(), None);
    let w = send(
        &a,
        "POST",
        "/api/update",
        "",
        r#"{"id":"e1","stack":"a","status":"definitely-not-a-status"}"#,
    )
    .await;
    assert_eq!(w.status, 400);
}

#[tokio::test]
async fn finalize_failed_marks_init_statuses_aborted() {
    let (_d, a, _gh) = app_of(gh_check_run(1), None);
    post_json(
        &a,
        "/api/init",
        &exec_init(
            "exec-init-sweep",
            10,
            "staging",
            "",
            vec![
                stack("a", "", Status::Initializing),
                stack("b", "", Status::Initialized),
                stack("c", "", Status::Planned),
                stack("d", "", Status::Moving),
            ],
        ),
    )
    .await;
    post_json(&a, "/api/finalize", &failed("exec-init-sweep")).await;
    let got = statuses(&a, "exec-init-sweep");
    assert_eq!(got["a"], Status::Aborted, "initializing");
    assert_eq!(got["b"], Status::Aborted, "initialized");
    assert_eq!(got["c"], Status::Planned, "planned unchanged");
    assert_eq!(got["d"], Status::Aborted, "moving");
}

#[tokio::test]
async fn claims_release_handler() {
    let (_d, a, _gh) = app_of(MockGitHub::default(), None);
    seed_claim(&a);
    assert_eq!(a.store().list_claims("prod").expect("list").len(), 1);
    let w = send(
        &a,
        "POST",
        "/api/claims/release",
        "",
        r#"{"environment":"prod","pr":42,"stack":""}"#,
    )
    .await;
    assert_eq!(w.status, 200);
    assert!(a.store().list_claims("prod").expect("list").is_empty());
}

#[tokio::test]
async fn init_recovers_pr_from_sha_when_missing() {
    let (_d, a) = run_trigger_app(run_trigger_gh(), Arc::new(FakeExecutor::default()));
    seed_init(
        &a,
        Init {
            id: "run-12-nonprod-plan-deadbeefcafe-a1".into(),
            repo: "o/r".into(),
            sha: "deadbeefcafe".into(),
            pr: 12,
            environment: "nonprod".into(),
            context: "plan/nonprod".into(),
            ..Init::default()
        },
    )
    .await;
    post_json(
        &a,
        "/api/init",
        &Init {
            id: "orphan-xyz".into(),
            repo: "o/r".into(),
            sha: "deadbeefcafe".into(),
            environment: "nonprod".into(),
            ..Init::default()
        },
    )
    .await;
    let got = a
        .store()
        .get_execution("orphan-xyz")
        .expect("get")
        .expect("row");
    assert_eq!(got.pr, 12, "recovered PR");
    let old = a
        .store()
        .get_execution("run-12-nonprod-plan-deadbeefcafe-a1")
        .expect("get")
        .expect("row");
    assert_eq!(old.superseded_by, "orphan-xyz");
}

#[tokio::test]
async fn claims_release_handler_bad_request() {
    let (_d, a, _gh) = app_of(MockGitHub::default(), None);
    let w = send(&a, "POST", "/api/claims/release", "", "{invalid-json}").await;
    assert_eq!(w.status, 400);
}

#[tokio::test]
async fn handle_init_projects_via_aggregate() {
    let (_d, a, _gh) = app_of(gh_check_run(1), None);
    post_json(
        &a,
        "/api/init",
        &Init {
            stacks: vec![stack("a", "", Status::Unset), stack("b", "", Status::Unset)],
            edges: vec![tfstackplan_gate::events::Edge {
                from: "a".into(),
                to: "b".into(),
            }],
            ..exec_init("e1", 7, "staging", "", vec![])
        },
    )
    .await;
    assert!(a.store().get_execution("e1").expect("get").is_some());
    let g = a.store().load_graph("e1").expect("graph");
    assert_eq!((g.stacks.len(), g.edges.len()), (2, 1));
    let (st, _) = a
        .store()
        .load::<tfstackplan_gate::execution::Execution>(&tfstackplan_store::run_stream_id("e1"))
        .expect("replay");
    assert_eq!(st.pr, 7);
    assert_eq!(st.stacks.len(), 2);
}
