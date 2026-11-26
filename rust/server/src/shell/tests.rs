// Ported from internal/server: shell_test.go, shell_concurrency_test.go,
// shell_eventstore_test.go, shell_exec_test.go, shell_execstream_test.go,
// shell_execute_test.go, shell_gather_test.go, shell_handle_flow_test.go,
// shell_handle_test.go, shell_overlay_test.go, shell_run_test.go,
// shell_save_test.go, claimshell_test.go, and the supersede/revive cases of
// handlers_test.go (TestSupersedeIsEventSourced, TestReInitRevivesThroughAggregate).
//
// Go's `sh.gather` is the store's `load_gate`. Go's `sh.project(cs, nil)` writes
// the projection of an arbitrary ChangeSet; the store crate projects only folded
// streams, so those cases append the events that fold to that ChangeSet and
// rebuild the gate's projection (`rebuild_gate` projects with no batch events,
// which is what `project(cs, nil)` does).
use std::collections::BTreeMap;
use std::sync::Arc;
use std::sync::atomic::{AtomicUsize, Ordering};

use jiff::{SignedDuration, Timestamp};
use tfstackplan_gate::approval::{GrantState, Request, SlotCollisionError};
use tfstackplan_gate::claims::{self, AcquireClaim, ReleaseClaim, ReleaseClaimStack};
use tfstackplan_gate::events::{GateTarget, Init, Phase, StackState, Status};
use tfstackplan_gate::execution::{
    self, Execution, ReportAnnotate, ReportInit, ReportPhase, ReportSucceed, ReportTick,
};
use tfstackplan_gate::reconcile::{
    self, AdoptRun, ChangeSet, Classified, Event, GateSatisfied, GateState, GateTick,
    GrantObserved, ObservedGrant, PRClosed, RUN_KIND_PLAN, RequestGrant, RevokeGrant,
    RunnerFinalize, Signal,
};
use tfstackplan_server_clients::Error as ClientError;
use tfstackplan_server_clients::approval::{Backend as _, Fake, Grant};
use tfstackplan_server_clients::github::MockGitHub;
use tfstackplan_store::{Store, StoredEvent, exec_stream_id, run_stream_id};

use crate::testutil::{
    FakeExecutor, active_claimed_stacks, app, run_trigger_app, run_trigger_gh, seed_init,
};
use crate::{App, Config};

fn app_with_fake(pool: &[&str]) -> (tempfile::TempDir, Arc<App>, Arc<Fake>) {
    let (d, s) = crate::testutil::store();
    let fake = Arc::new(Fake::with_pool(
        pool.iter().map(|p| (*p).to_string()).collect(),
    ));
    let mut a = App::new(s, Arc::new(MockGitHub::default()), Config::default());
    a.approval = Some(fake.clone());
    (d, Arc::new(a), fake)
}

fn finalize(targets: &[&str]) -> Signal {
    RunnerFinalize {
        gates: targets
            .iter()
            .map(|t| GateTarget {
                class: "iam".into(),
                target: (*t).into(),
            })
            .collect(),
        apply_context: false,
    }
    .into()
}

fn init(id: &str, pr: i64, env: &str, repo: &str) -> Init {
    Init {
        id: id.into(),
        pr,
        environment: env.into(),
        repo: repo.into(),
        ..Init::default()
    }
}

async fn gather(a: &App, pr: i64, env: &str) -> (ChangeSet, i64) {
    let env = env.to_string();
    a.db(move |s| s.load_gate(pr, &env)).await.expect("gather")
}

/// Go's applyAllowed: only Clean or Satisfied permit apply.
fn apply_allowed(g: &GateState) -> bool {
    matches!(g, GateState::Clean | GateState::Satisfied(_))
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

// --- shell_test.go ---

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn shell_lock_serializes_per_change_set() {
    let (_d, a) = app();
    let concurrent = Arc::new(AtomicUsize::new(0));
    let overlaps = Arc::new(AtomicUsize::new(0));
    let mut tasks = Vec::new();
    for _ in 0..20 {
        let (a, c, o) = (
            Arc::clone(&a),
            Arc::clone(&concurrent),
            Arc::clone(&overlaps),
        );
        tasks.push(tokio::spawn(async move {
            let _g = a.gate_locks.lock((7, "staging".to_string())).await;
            if c.fetch_add(1, Ordering::SeqCst) + 1 > 1 {
                o.fetch_add(1, Ordering::SeqCst);
            }
            tokio::task::yield_now().await;
            c.fetch_sub(1, Ordering::SeqCst);
        }));
    }
    for t in tasks {
        t.await.expect("task");
    }
    assert_eq!(
        overlaps.load(Ordering::SeqCst),
        0,
        "overlapping critical sections"
    );
}

// --- shell_concurrency_test.go ---

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn concurrent_finalize_and_tick_no_premature_satisfied() {
    let (_d, a, _fake) = app_with_fake(&["sa0", "sa1"]);
    seed_init(&a, init("e1", 7, "staging", "r")).await;
    let (f, t) = tokio::join!(
        a.handle(7, "staging", "r", finalize(&["p1", "p2"])),
        a.handle(7, "staging", "r", GateTick::default().into()),
    );
    let _ = (f, t);
    let targets = a
        .db(|s| s.targets_for(7, "staging"))
        .await
        .expect("targets");
    if targets.len() == 2 {
        assert_eq!(
            targets[0].requester, targets[1].requester,
            "inconsistent lease across targets"
        );
    }
}

// --- shell_eventstore_test.go ---

#[tokio::test]
async fn shell_appends_and_replays_gate() {
    let (_d, a) = app();
    a.handle(7, "nonprod", "repo", RunnerFinalize::default().into())
        .await
        .expect("handle finalize");
    let (cs, version) = gather(&a, 7, "nonprod").await;
    assert_ne!(version, 0, "expected a non-empty stream after finalize");
    assert_eq!(cs.gate, GateState::Clean);
}

#[tokio::test]
async fn shell_gather_empty_stream_is_not_classified() {
    let (_d, a) = app();
    let (cs, version) = gather(&a, 99, "nonprod").await;
    assert_eq!(version, 0);
    assert_eq!(cs.gate, GateState::NotClassified);
}

// --- shell_exec_test.go ---

#[tokio::test]
async fn handle_exec_projects_full_lifecycle() {
    let (_d, a) = app();
    let id = "run-7-nonprod-plan-abc-a1";
    let sigs: Vec<execution::Signal> = vec![
        ReportInit {
            exec: execution::State {
                id: id.into(),
                pr: 7,
                environment: "nonprod".into(),
                context: "terraform/nonprod".into(),
                repo: "r".into(),
                sha: "abc".into(),
                stacks: vec![
                    execution::Stack {
                        path: "a".into(),
                        ..execution::Stack::default()
                    },
                    execution::Stack {
                        path: "b".into(),
                        ..execution::Stack::default()
                    },
                ],
                edges: vec![execution::Edge {
                    from: "a".into(),
                    to: "b".into(),
                }],
                ..execution::State::default()
            },
        }
        .into(),
        ReportPhase {
            id: id.into(),
            phase: Phase::new(Phase::APPLYING),
            label: "applying".into(),
            ..ReportPhase::default()
        }
        .into(),
        ReportTick {
            stack: "a".into(),
            status: Status::Running,
            detail: String::new(),
        }
        .into(),
        ReportAnnotate {
            projects: BTreeMap::from([("a".to_string(), "proj-a".to_string())]),
            ..ReportAnnotate::default()
        }
        .into(),
        ReportSucceed {}.into(),
    ];
    for sig in sigs {
        a.handle_exec(id, sig).await.expect("handle exec");
    }
    let s = a.store();
    let e = s.get_execution(id).expect("get").expect("row");
    assert_eq!(
        (e.pr, e.environment.as_str(), e.status_context.as_str()),
        (7, "nonprod", "terraform/nonprod"),
        "identity not projected"
    );
    assert_eq!(
        (e.phase.as_str(), e.status.as_str()),
        ("applying", "success")
    );
    let g = s.load_graph(id).expect("graph");
    assert_eq!((g.stacks.len(), g.edges.len()), (2, 1));
    assert_eq!(g.stacks[0].project, "proj-a");
    assert_eq!(g.stacks[0].status, Status::Running);
    let ph = s.phases_for(id).expect("phases");
    assert_eq!(ph.len(), 1);
    assert_eq!(ph[0].phase, "applying");
    // Replay proves the stream (not just the projection) is the source of truth.
    let (st, _) = s.load::<Execution>(&run_stream_id(id)).expect("load");
    assert_eq!(st.status, "success");
    assert_eq!(st.pr, 7);
    assert_eq!(st.stacks.len(), 2);
}

// --- shell_execstream_test.go ---

#[test]
fn exec_decider_round_trips_through_store() {
    let (_d, s) = crate::testutil::store();
    let id = "run-1-nonprod-plan-abc-a1";
    let stream = run_stream_id(id);
    let mut state = execution::State::default();
    let mut version = 0;
    let sigs: Vec<execution::Signal> = vec![
        ReportInit {
            exec: execution::State {
                id: id.into(),
                stacks: vec![execution::Stack {
                    path: "a".into(),
                    ..execution::Stack::default()
                }],
                ..execution::State::default()
            },
        }
        .into(),
        ReportTick {
            stack: "a".into(),
            status: Status::Running,
            detail: String::new(),
        }
        .into(),
    ];
    for sig in sigs {
        let evs = execution::decide(&state, &sig);
        let new_state = evs.iter().fold(state.clone(), execution::evolve);
        s.append::<Execution>(&stream, version, &evs, &new_state)
            .expect("append");
        version += i64::try_from(evs.len()).expect("len");
        state = new_state;
    }
    let (got, got_ver) = s.load::<Execution>(&stream).expect("load");
    assert_eq!(got_ver, 2);
    assert_eq!(got.id, id);
    assert_eq!(got.stacks.len(), 1);
    assert_eq!(got.stacks[0].run_status, Status::Running);
}

// --- shell_execute_test.go ---

fn staging_cs() -> ChangeSet {
    ChangeSet {
        pr: 7,
        environment: "staging".into(),
        ..ChangeSet::default()
    }
}

fn request_p1() -> RequestGrant {
    RequestGrant {
        class: "iam".into(),
        target: "p1".into(),
        requester: String::new(),
    }
}

#[tokio::test]
async fn execute_request_grant_yields_observed_grant() {
    let (_d, a, _fake) = app_with_fake(&[]);
    let feedback = a
        .execute(&staging_cs(), "repo", vec![request_p1().into()])
        .await;
    assert_eq!(feedback.len(), 1, "want one feedback signal: {feedback:?}");
    let Signal::GrantsObserved(obs) = &feedback[0] else {
        panic!("want GrantsObserved, got {:?}", feedback[0]);
    };
    assert_eq!(obs.grants.len(), 1);
    assert_eq!(obs.grants[0].target, "p1");
    assert_ne!(obs.grants[0].state, GrantState::Absent);
}

#[tokio::test]
async fn execute_revoke_yields_no_result() {
    let (_d, a, _fake) = app_with_fake(&[]);
    let feedback = a
        .execute(
            &staging_cs(),
            "repo",
            vec![
                RevokeGrant {
                    class: "iam".into(),
                    target: "p1".into(),
                    pr: 7,
                    environment: "staging".into(),
                }
                .into(),
            ],
        )
        .await;
    assert!(
        feedback.is_empty(),
        "revoke must yield no feedback: {feedback:?}"
    );
}

fn collision(pr: i64) -> ClientError {
    SlotCollisionError {
        blocking_grant: Grant {
            request: req("", pr, "staging"),
            ..Grant::default()
        },
    }
    .into()
}

fn abandoned_gh(called: Arc<AtomicUsize>, abandoned_pr: Option<i64>) -> MockGitHub {
    let mut gh = MockGitHub::default();
    gh.pr_abandoned_fn = Some(Box::new(move |(_, pr)| {
        if abandoned_pr.is_none_or(|p| p == pr) {
            called.fetch_add(1, Ordering::SeqCst);
        }
        Ok(abandoned_pr == Some(pr))
    }));
    gh
}

#[tokio::test]
async fn observe_error_not_collision() {
    let (_d, a) = app();
    let obs = a
        .observe_error(
            &staging_cs(),
            "repo",
            &request_p1(),
            ClientError::msg("internal error"),
        )
        .await;
    assert_eq!(obs.state, GrantState::Absent);
    assert!(obs.collision.is_none());
}

#[tokio::test]
async fn observe_error_collision_self() {
    let (_d, a) = app();
    let obs = a
        .observe_error(&staging_cs(), "repo", &request_p1(), collision(7))
        .await;
    let c = obs.collision.expect("collision");
    assert!(c.by_self, "BySelf for a collision with the same PR");
    assert!(!c.by_pr_abandoned, "ByPRAbandoned false for self-collision");
}

#[tokio::test]
async fn observe_error_collision_other_pr() {
    let called = Arc::new(AtomicUsize::new(0));
    let (_d, a) = crate::testutil::app_with(
        Arc::new(abandoned_gh(Arc::clone(&called), Some(5))),
        Config::default(),
    );
    let obs = a
        .observe_error(&staging_cs(), "repo", &request_p1(), collision(5))
        .await;
    let c = obs.collision.expect("collision");
    assert!(!c.by_self);
    assert!(c.by_pr_abandoned, "ByPRAbandoned from the mock's answer");
    assert_eq!(
        called.load(Ordering::SeqCst),
        1,
        "PRAbandoned must be called"
    );
}

#[tokio::test]
async fn observe_error_collision_pr_zero() {
    let called = Arc::new(AtomicUsize::new(0));
    let (_d, a) = crate::testutil::app_with(
        Arc::new(abandoned_gh(Arc::clone(&called), None)),
        Config::default(),
    );
    let obs = a
        .observe_error(&staging_cs(), "repo", &request_p1(), collision(0))
        .await;
    let c = obs.collision.expect("collision");
    assert!(!c.by_self);
    assert!(!c.by_pr_abandoned);
    assert_eq!(
        called.load(Ordering::SeqCst),
        0,
        "PRAbandoned must not be called for blocking PR 0"
    );
}

// --- shell_gather_test.go ---

#[tokio::test]
async fn gather_replays_satisfied() {
    let (_d, a, _fake) = app_with_fake(&["sa0", "sa1"]);
    seed_init(&a, init("e1", 7, "staging", "r")).await;
    a.handle(7, "staging", "r", finalize(&["p1"]))
        .await
        .expect("finalize");
    let (world, _) = gather(&a, 7, "staging").await;
    let requester = reconcile::requester(&world.gate);
    assert!(
        !requester.is_empty(),
        "want a leased gate after finalize, got {:?}",
        world.gate
    );
    a.handle(
        7,
        "staging",
        "r",
        GateTick {
            grants: vec![ObservedGrant {
                class: "iam".into(),
                target: "p1".into(),
                name: "g1".into(),
                state: GrantState::Active,
                requester,
                collision: None,
            }],
        }
        .into(),
    )
    .await
    .expect("tick");
    let (w2, _) = gather(&a, 7, "staging").await;
    let GateState::Satisfied(sat) = w2.gate else {
        panic!("want Satisfied after all-ACTIVE re-list, got {:?}", w2.gate);
    };
    assert_eq!(sat.targets.len(), 1);
    assert_eq!(sat.targets[0].target, "p1");
}

#[tokio::test]
async fn gather_replays_not_classified_for_empty_stream() {
    let (_d, a) = app();
    assert_eq!(
        gather(&a, 7, "staging").await.0.gate,
        GateState::NotClassified
    );
}

#[tokio::test]
async fn gather_replays_clean_for_gateless_finalize() {
    let (_d, a) = app();
    a.handle(7, "staging", "r", RunnerFinalize::default().into())
        .await
        .expect("finalize");
    assert_eq!(gather(&a, 7, "staging").await.0.gate, GateState::Clean);
}

// --- shell_handle_flow_test.go ---

async fn flow_app() -> (tempfile::TempDir, Arc<App>, Arc<Fake>) {
    let (d, a, fake) = app_with_fake(&["sa0"]);
    seed_init(&a, init("e1", 7, "staging", "r")).await;
    (d, a, fake)
}

#[tokio::test]
async fn handle_approve_then_tick_becomes_apply_allowed() {
    let (_d, a, fake) = flow_app().await;
    a.handle(7, "staging", "r", finalize(&["p1"]))
        .await
        .expect("finalize");
    let w0 = gather(&a, 7, "staging").await.0;
    assert!(
        !apply_allowed(&w0.gate),
        "apply blocked before approval: {:?}",
        w0.gate
    );
    fake.approve(&req("p1", 7, "staging"));
    let grants = fake.list_grants("iam", "p1").await.expect("list");
    let obs = grants
        .into_iter()
        .filter(|g| g.request.pr == 7 && g.request.environment == "staging")
        .map(|g| ObservedGrant {
            class: "iam".into(),
            target: "p1".into(),
            name: g.name,
            state: g.state,
            requester: g.requester,
            collision: None,
        })
        .collect();
    a.handle(7, "staging", "r", GateTick { grants: obs }.into())
        .await
        .expect("tick");
    let w1 = gather(&a, 7, "staging").await.0;
    assert!(
        apply_allowed(&w1.gate),
        "apply allowed after approval: {:?}",
        w1.gate
    );
}

#[tokio::test]
async fn handle_pr_closed_revokes_and_blocks() {
    let (_d, a, fake) = flow_app().await;
    a.handle(7, "staging", "r", finalize(&["p1"]))
        .await
        .expect("finalize");
    a.handle(7, "staging", "r", PRClosed {}.into())
        .await
        .expect("pr closed");
    for g in fake.list_grants("iam", "p1").await.expect("list") {
        assert!(
            !g.state.open(),
            "want grant revoked after PRClosed, got {:?}",
            g.state
        );
    }
    let w = gather(&a, 7, "staging").await.0;
    assert!(
        !apply_allowed(&w.gate),
        "apply blocked after PR closed: {:?}",
        w.gate
    );
}

// --- shell_handle_test.go ---

#[tokio::test]
async fn handle_finalize_requests_all_gates_via_fixpoint() {
    let (_d, a, _fake) = app_with_fake(&["sa0", "sa1"]);
    seed_init(&a, init("e1", 7, "staging", "r")).await;
    a.handle(7, "staging", "r", finalize(&["p1", "p2"]))
        .await
        .expect("finalize");
    let targets = a.store().targets_for(7, "staging").expect("targets");
    assert_eq!(targets.len(), 2);
    assert!(!targets[0].requester.is_empty());
    assert_eq!(
        targets[0].requester, targets[1].requester,
        "both targets share one lease"
    );
}

// --- shell_overlay_test.go ---

/// Appends `evs` to (pr, env)'s gate stream and rebuilds its projection: the
/// store-API stand-in for Go's `sh.project(cs, nil)` of the folded ChangeSet.
fn project(s: &Store, pr: i64, env: &str, evs: &[Event]) {
    let stream = exec_stream_id(pr, env);
    let (_, version) = s.load_events(&stream).expect("load events");
    let stored: Vec<StoredEvent> = evs
        .iter()
        .map(|e| {
            let (kind, data) = reconcile::marshal_event(e).expect("marshal");
            StoredEvent {
                kind: kind.to_string(),
                data,
            }
        })
        .collect();
    s.append_events(&stream, version, &stored)
        .expect("append events");
    s.rebuild_gate(pr, env).expect("rebuild gate");
}

/// Events folding to `Pending{[iam/p1]}`.
fn pending_p1() -> Vec<Event> {
    vec![
        Classified {
            gates: vec![GateTarget {
                class: "iam".into(),
                target: "p1".into(),
            }],
        }
        .into(),
    ]
}

/// Events folding to `Satisfied{[iam/p1 g1 ACTIVE]}` with `requester`'s lease.
fn satisfied_p1(requester: &str) -> Vec<Event> {
    let mut evs = pending_p1();
    evs.push(
        GrantObserved {
            class: "iam".into(),
            target: "p1".into(),
            name: "g1".into(),
            state: GrantState::Active,
            requester: requester.into(),
        }
        .into(),
    );
    evs.push(GateSatisfied {}.into());
    evs
}

fn stack_status(s: &Store, exec_id: &str, path: &str) -> String {
    let g = s.load_graph(exec_id).expect("graph");
    g.stacks
        .iter()
        .find(|st| st.path == path)
        .map(|st| st.status.as_str().to_string())
        .unwrap_or_else(|| panic!("stack {path:?} not found"))
}

fn stack(path: &str, project: &str, status: Status) -> StackState {
    StackState {
        path: path.into(),
        project: project.into(),
        status,
        ..StackState::default()
    }
}

async fn seed_exec(a: &App, with_aborted_and_moving: bool) {
    let mut stacks = vec![
        stack("s1", "p1", Status::Planned),
        stack("s2", "other", Status::Planned),
        stack("s3", "p1", Status::Failed),
    ];
    if with_aborted_and_moving {
        stacks.push(stack("s4", "p1", Status::Aborted));
        stacks.push(stack("s5", "p1", Status::Moving));
    }
    seed_init(
        a,
        Init {
            stacks,
            ..init("e1", 7, "staging", "r")
        },
    )
    .await;
}

#[tokio::test]
async fn save_writes_gated_overlay() {
    let (_d, a) = app();
    seed_exec(&a, false).await;
    let s = a.store();
    project(s, 7, "staging", &pending_p1());
    assert_eq!(stack_status(s, "e1", "s1"), "gated");
    assert_eq!(stack_status(s, "e1", "s2"), "planned", "ungated stays");
    assert_eq!(stack_status(s, "e1", "s3"), "failed", "failed wins");
}

#[tokio::test]
async fn save_writes_safe_overlay_when_satisfied() {
    let (_d, a) = app();
    seed_exec(&a, false).await;
    let s = a.store();
    project(s, 7, "staging", &satisfied_p1(""));
    assert_eq!(stack_status(s, "e1", "s1"), "safe");
    assert_eq!(stack_status(s, "e1", "s3"), "failed", "failed wins");
}

#[tokio::test]
async fn save_overlay_skips_aborted() {
    let (_d, a) = app();
    seed_exec(&a, true).await;
    let s = a.store();
    project(s, 7, "staging", &pending_p1());
    assert_eq!(
        stack_status(s, "e1", "s4"),
        "aborted",
        "aborted wins over gated"
    );
    // Pending → Satisfied on the same stream.
    project(
        s,
        7,
        "staging",
        &satisfied_p1("")[1..], // the GrantObserved and GateSatisfied
    );
    assert_eq!(
        stack_status(s, "e1", "s4"),
        "aborted",
        "aborted wins over safe"
    );
}

#[tokio::test]
async fn save_overlay_wins_over_moving() {
    let (_d, a) = app();
    seed_exec(&a, true).await;
    project(a.store(), 7, "staging", &pending_p1());
    assert_eq!(
        stack_status(a.store(), "e1", "s5"),
        "gated",
        "gated wins over moving"
    );

    let (_d2, a2) = app();
    seed_exec(&a2, true).await;
    project(a2.store(), 7, "staging", &satisfied_p1(""));
    assert_eq!(
        stack_status(a2.store(), "e1", "s5"),
        "safe",
        "safe wins over moving"
    );
}

// --- shell_run_test.go ---

#[tokio::test]
async fn adopt_run_materializes_without_executor() {
    let fe = Arc::new(FakeExecutor::default());
    let (_d, a) = run_trigger_app(run_trigger_gh(), fe.clone());
    let cs = ChangeSet {
        pr: 21,
        environment: "nonprod".into(),
        ..ChangeSet::default()
    };
    a.adopt_run(
        &cs,
        "o/r",
        AdoptRun {
            kind: RUN_KIND_PLAN.into(),
            execution_id: "run-21-nonprod-plan-abc-a2".into(),
            sha: "abcsha".into(),
            build_ref: "build-rerun".into(),
        },
    )
    .await;
    let e = a
        .store()
        .get_execution("run-21-nonprod-plan-abc-a2")
        .expect("get")
        .expect("adopted execution missing");
    assert_eq!(e.pr, 21);
    assert_eq!(e.sha, "abcsha");
    assert_eq!(e.status_context, "plan/nonprod");
    assert!(
        e.check_run_id.is_some_and(|c| c != 0),
        "adopted execution has no check run"
    );
    assert!(
        fe.starts.lock().expect("lock").is_empty(),
        "executor.Start called during adopt"
    );
}

// --- shell_save_test.go ---

#[tokio::test]
async fn project_persists_targets_and_prunes() {
    let (_d, a, _fake) = app_with_fake(&["sa0"]);
    seed_init(&a, init("e1", 7, "staging", "r")).await;
    a.handle(7, "staging", "r", finalize(&["p1", "p2"]))
        .await
        .expect("finalize");
    let s = a.store();
    let before = s.targets_for(7, "staging").expect("targets");
    assert_eq!(before.len(), 2, "pre-prune: {before:?}");

    // Re-classify to only p1, approved: p2 must be pruned. Go projects a
    // hand-built ChangeSet leased to "sa3"; through the fold the lease is sticky,
    // so the persisted requester is the gate's lease, sa0.
    project(s, 7, "staging", &satisfied_p1("sa3"));
    let targets = s.targets_for(7, "staging").expect("targets");
    assert_eq!(targets.len(), 1, "want only p1 retained: {targets:?}");
    assert_eq!(targets[0].target, "p1");
    assert_eq!(targets[0].requester, "sa0", "requester persisted");
}

// --- claimshell_test.go ---

fn noon() -> Timestamp {
    "2026-01-01T12:00:00Z".parse().expect("time")
}

fn stacks(s: &[&str]) -> Vec<String> {
    s.iter().map(|x| (*x).to_string()).collect()
}

async fn acquire(a: &App, env: &str, pr: i64, s: &[&str], now: Timestamp) {
    a.handle_claim(
        env,
        AcquireClaim {
            pr,
            stacks: stacks(s),
            now,
        }
        .into(),
    )
    .await
    .expect("acquire");
}

fn clock_app(now: Timestamp) -> (tempfile::TempDir, Arc<App>) {
    let (d, s) = crate::testutil::store();
    let mut a = App::new(
        s,
        Arc::new(crate::testutil::recording_github()),
        Config {
            public_base_url: "https://srv".into(),
            ..Config::default()
        },
    );
    a.now = Arc::new(move || now);
    (d, Arc::new(a))
}

#[tokio::test]
async fn handle_claim_acquire_projects_and_folds() {
    let now = noon();
    let (_d, a) = clock_app(now);
    acquire(&a, "prod", 7, &["a", "b"], now).await;
    let cs = a.store().load_claims("prod").expect("load claims");
    assert_eq!((cs["a"].pr, cs["b"].pr), (7, 7));
    assert!(cs["a"].expires_at > now, "lease not set");
    let proj = active_claimed_stacks(a.store(), "prod", now);
    assert_eq!(
        proj,
        BTreeMap::from([("a".to_string(), 7), ("b".to_string(), 7)])
    );
}

#[tokio::test]
async fn handle_claim_release_clears_both() {
    let now = noon();
    let (_d, a) = clock_app(now);
    acquire(&a, "prod", 7, &["a", "b"], now).await;
    a.handle_claim("prod", ReleaseClaim { pr: 7 }.into())
        .await
        .expect("release");
    assert!(a.store().load_claims("prod").expect("load").is_empty());
    assert!(active_claimed_stacks(a.store(), "prod", now).is_empty());
}

#[tokio::test]
async fn handle_claim_release_stack() {
    let now = noon();
    let (_d, a) = clock_app(now);
    acquire(&a, "prod", 7, &["a", "b"], now).await;
    a.handle_claim(
        "prod",
        ReleaseClaimStack {
            pr: 7,
            stack: "a".into(),
        }
        .into(),
    )
    .await
    .expect("release stack");
    let cs = a.store().load_claims("prod").expect("load");
    assert!(!cs.contains_key("a"), "stack a still claimed: {cs:?}");
    assert_eq!(cs["b"].pr, 7);
    assert_eq!(
        active_claimed_stacks(a.store(), "prod", now),
        BTreeMap::from([("b".to_string(), 7)])
    );
}

#[tokio::test]
async fn eval_apply_lock_overlap_from_fold() {
    let now = noon();
    let (_d, a) = clock_app(now);
    acquire(&a, "prod", 5, &["a"], now).await;
    let v = a
        .eval_apply_lock("prod", 7, &stacks(&["a", "b"]), now)
        .await;
    assert_eq!(v.state, "held");
    assert_eq!(v.blocking, stacks(&["a"]));
    assert_eq!(
        a.eval_apply_lock("prod", 5, &stacks(&["a"]), now)
            .await
            .state,
        "clear",
        "own claim"
    );
    assert_eq!(
        a.eval_apply_lock("prod", 9, &stacks(&["c", "d"]), now)
            .await
            .state,
        "clear",
        "disjoint"
    );
    let later = now + claims::LEASE + SignedDuration::from_mins(1);
    assert_eq!(
        a.eval_apply_lock("prod", 7, &stacks(&["a"]), later)
            .await
            .state,
        "clear",
        "expiry enforced at read"
    );
}

// --- handlers_test.go: supersede and revive through the execution aggregate ---

async fn mk_init(a: &App, id: &str, status: &str) {
    a.handle_exec(
        id,
        ReportInit {
            exec: execution::State {
                id: id.into(),
                pr: 7,
                environment: "nonprod".into(),
                repo: "r".into(),
                status: status.into(),
                stacks: vec![execution::Stack {
                    path: "a".into(),
                    ..execution::Stack::default()
                }],
                ..execution::State::default()
            },
        }
        .into(),
    )
    .await
    .expect("init");
}

#[tokio::test]
async fn supersede_is_event_sourced() {
    let (_d, a) = app();
    let (old, nw) = ("run-7-nonprod-plan-aaa-a1", "run-7-nonprod-plan-bbb-a1");
    mk_init(&a, old, "").await;
    mk_init(&a, nw, "").await;
    a.supersede_execution(old, nw).await;
    let e = a.store().get_execution(old).expect("get").expect("row");
    assert_eq!(e.superseded_by, nw);
    a.handle_exec(
        old,
        ReportTick {
            stack: "a".into(),
            status: Status::Failed,
            detail: String::new(),
        }
        .into(),
    )
    .await
    .expect("late tick");
    let e2 = a.store().get_execution(old).expect("get").expect("row");
    assert_eq!(e2.superseded_by, nw, "late tick clobbered superseded_by");
}

#[tokio::test]
async fn re_init_revives_through_aggregate() {
    let (_d, a) = app();
    let id = "run-7-nonprod-plan-ccc-a1";
    mk_init(&a, id, "in_progress").await;
    a.handle_exec(id, ReportSucceed {}.into())
        .await
        .expect("succeed");
    a.supersede_execution(id, "some-newer").await;
    mk_init(&a, id, "in_progress").await;
    let e = a.store().get_execution(id).expect("get").expect("row");
    assert_eq!(e.status, "in_progress");
    assert_eq!(e.superseded_by, "");
}
