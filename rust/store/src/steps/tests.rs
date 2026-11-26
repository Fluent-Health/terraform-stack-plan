// The shell's persist and project round (Go: shell_save_test.go,
// shell_concurrency_test.go, claimshell_test.go in spirit), the projection
// rebuild, and per-key serialization under contention.
use std::sync::mpsc;
use std::thread;
use std::time::Duration;

use jiff::Timestamp;
use tfstackplan_gate::approval::GrantState;
use tfstackplan_gate::claims::{AcquireClaim, ReleaseClaim, RenewClaim};
use tfstackplan_gate::events::{GateTarget, Phase, Status};
use tfstackplan_gate::execution::{ReportFail, ReportInit, ReportPhase, ReportTick, Stack};
use tfstackplan_gate::reconcile::{
    self, ApplySucceeded, GateState, GrantsObserved, ObservedGrant, RUN_KIND_PLAN, RunRequested,
    RunnerFinalize,
};

use super::*;
use crate::KeyedLocks;
use crate::db::tests::temp_store;

fn init(s: &Store, id: &str, pr: i64, env: &str, stacks: &[(&str, &str)]) {
    s.step_execution(
        id,
        &ReportInit {
            exec: execution::State {
                id: id.into(),
                repo: "o/r".into(),
                sha: "abc".into(),
                pr,
                environment: env.into(),
                status: "in_progress".into(),
                stacks: stacks
                    .iter()
                    .map(|(path, project)| Stack {
                        path: path.to_string(),
                        project: project.to_string(),
                        run_status: Status::Pending,
                        ..Stack::default()
                    })
                    .collect(),
                ..execution::State::default()
            },
        }
        .into(),
    )
    .unwrap();
}

fn finalize(gates: &[(&str, &str)]) -> Signal {
    RunnerFinalize {
        gates: gates
            .iter()
            .map(|(class, target)| GateTarget {
                class: class.to_string(),
                target: target.to_string(),
            })
            .collect(),
        apply_context: false,
    }
    .into()
}

fn observed(target: &str, name: &str, state: GrantState) -> Signal {
    GrantsObserved {
        grants: vec![ObservedGrant {
            class: "iam".into(),
            target: target.into(),
            name: name.into(),
            state,
            requester: "sa0".into(),
            collision: None,
        }],
    }
    .into()
}

#[test]
fn gate_round_persists_projects_and_reacts() {
    let (_dir, s) = temp_store();
    init(
        &s,
        "e1",
        7,
        "staging",
        &[("stacks/a", "p1"), ("stacks/b", "p2")],
    );

    let step = s
        .step_gate(7, "staging", &finalize(&[("iam", "p1")]))
        .unwrap();
    assert!(matches!(step.state.gate, GateState::Pending(_)));
    assert!(
        step.actions
            .iter()
            .any(|a| matches!(a, Action::RequestGrant(r) if r.target == "p1")),
        "{:?}",
        step.actions
    );
    let rows = s.targets_for(7, "staging").unwrap();
    assert_eq!((rows.len(), rows[0].state.as_str()), (1, ""));
    // The pending gate overlays its stacks as gated.
    let g = s.load_graph("e1").unwrap();
    assert_eq!(g.stacks[0].status, Status::Gated);
    assert_eq!(g.stacks[1].status, Status::Pending);

    let step = s
        .step_gate(7, "staging", &observed("p1", "g1", GrantState::Active))
        .unwrap();
    assert!(matches!(step.state.gate, GateState::Satisfied(_)));
    let rows = s.targets_for(7, "staging").unwrap();
    assert_eq!(
        (
            rows[0].grant_name.as_str(),
            rows[0].state.as_str(),
            rows[0].requester.as_str()
        ),
        ("g1", "ACTIVE", "sa0")
    );
    assert_eq!(s.load_graph("e1").unwrap().stacks[0].status, Status::Safe);

    let step = s
        .step_gate(7, "staging", &ApplySucceeded {}.into())
        .unwrap();
    assert_eq!(step.state.gate, GateState::Clean);
    assert!(
        step.actions
            .iter()
            .any(|a| matches!(a, Action::ReleaseClaim(_)))
    );
    assert!(s.targets_for(7, "staging").unwrap().is_empty());

    // The log replays to what the snapshot holds.
    let stream = exec_stream_id(7, "staging");
    let (replayed, version) = s.replay::<Gate>(&stream).unwrap();
    let (loaded, loaded_version) = s.load_gate(7, "staging").unwrap();
    assert_eq!((replayed.gate, version), (loaded.gate, loaded_version));
}

#[test]
fn start_failure_marks_its_execution_failed() {
    let (_dir, s) = temp_store();
    let step = s
        .step_gate(
            7,
            "staging",
            &RunRequested {
                kind: RUN_KIND_PLAN.into(),
                sha: "abcdef".into(),
                branch: "b".into(),
                rerun: false,
            }
            .into(),
        )
        .unwrap();
    let Some(Action::StartRun(start)) = step.actions.first() else {
        panic!("{:?}", step.actions)
    };
    // The shell materializes the queued execution before starting it.
    init(&s, &start.execution_id, 7, "staging", &[]);
    s.step_gate(
        7,
        "staging",
        &reconcile::RunStartResult {
            kind: RUN_KIND_PLAN.into(),
            execution_id: start.execution_id.clone(),
            build_ref: String::new(),
            err: "no executor configured".into(),
        }
        .into(),
    )
    .unwrap();
    let e = s.get_execution(&start.execution_id).unwrap().unwrap();
    assert_eq!(e.status, "failure");
}

#[test]
fn execution_round_projects_row_stacks_and_phases() {
    let (_dir, s) = temp_store();
    init(&s, "e1", 7, "staging", &[("stacks/a", "p1")]);
    s.step_execution(
        "e1",
        &ReportPhase {
            phase: Phase::new(Phase::APPLYING),
            label: "applying 1/1".into(),
            pct: Some(50),
            ..ReportPhase::default()
        }
        .into(),
    )
    .unwrap();
    s.step_execution(
        "e1",
        &ReportTick {
            stack: "stacks/a".into(),
            status: Status::Running,
            detail: String::new(),
        }
        .into(),
    )
    .unwrap();
    let state = s.step_execution("e1", &ReportFail {}.into()).unwrap();
    assert_eq!(state.status, "failure");
    let e = s.get_execution("e1").unwrap().unwrap();
    assert_eq!(
        (e.status.as_str(), e.phase.as_str(), e.progress_pct),
        ("failure", "applying", Some(50))
    );
    assert_eq!(
        s.load_graph("e1").unwrap().stacks[0].status,
        Status::Aborted
    );
    assert_eq!(s.phases_for("e1").unwrap().len(), 1);
}

#[test]
fn claim_rounds_project_the_ledger() {
    let (_dir, s) = temp_store();
    let now: Timestamp = "2026-06-24T12:00:00Z".parse().unwrap();
    s.step_claims(
        "prod",
        &AcquireClaim {
            pr: 7,
            stacks: vec!["a".into(), "b".into()],
            now,
        }
        .into(),
    )
    .unwrap();
    s.step_claims(
        "prod",
        &RenewClaim {
            pr: 7,
            now: now + claims::LEASE,
        }
        .into(),
    )
    .unwrap();
    let listed = s.list_claims("prod").unwrap();
    assert_eq!(listed.len(), 2);
    assert_eq!(listed[0].expires_at, now + claims::LEASE * 2);
    let held = claims::held(&s.load_claims("prod").unwrap(), 9, &["a".into()], now);
    assert!(held.held);
    s.step_claims("prod", &ReleaseClaim { pr: 7 }.into())
        .unwrap();
    assert!(s.list_claims("prod").unwrap().is_empty());
}

#[test]
fn rebuild_all_restores_every_projection() {
    let (_dir, s) = temp_store();
    init(&s, "e1", 7, "staging", &[("stacks/a", "p1")]);
    s.step_execution(
        "e1",
        &ReportPhase {
            phase: Phase::new(Phase::PLANNING),
            ..ReportPhase::default()
        }
        .into(),
    )
    .unwrap();
    s.step_gate(7, "staging", &finalize(&[("iam", "p1")]))
        .unwrap();
    s.step_claims(
        "staging",
        &AcquireClaim {
            pr: 7,
            stacks: vec!["stacks/a".into()],
            now: Timestamp::now(),
        }
        .into(),
    )
    .unwrap();
    let before = (
        s.get_execution("e1").unwrap().map(|e| (e.status, e.phase)),
        s.load_graph("e1").unwrap(),
        s.targets_for(7, "staging").unwrap(),
        s.list_claims("staging").unwrap(),
        s.phases_for("e1").unwrap().len(),
    );
    s.conn()
        .execute_batch(
            "DELETE FROM executions; DELETE FROM stacks; DELETE FROM edges;
             DELETE FROM gate_targets; DELETE FROM apply_claims; DELETE FROM execution_phases;",
        )
        .unwrap();
    s.rebuild_all().unwrap();
    let after = (
        s.get_execution("e1").unwrap().map(|e| (e.status, e.phase)),
        s.load_graph("e1").unwrap(),
        s.targets_for(7, "staging").unwrap(),
        s.list_claims("staging").unwrap(),
        s.phases_for("e1").unwrap().len(),
    );
    assert_eq!(before, after);
}

// --- serialization ---

const THREADS: usize = 8;
const ROUNDS: usize = 5;

fn run_request(t: usize, r: usize) -> Signal {
    RunRequested {
        kind: RUN_KIND_PLAN.into(),
        sha: format!("sha-{t}-{r}"),
        branch: "b".into(),
        rerun: false,
    }
    .into()
}

/// Run requests from many threads on one (pr, env), each under the key's lock:
/// every round lands, in some order, and the attempts count every request.
#[test]
fn parallel_signals_on_one_key_serialize() {
    let (_dir, s) = temp_store();
    let locks: KeyedLocks<(i64, String)> = KeyedLocks::default();
    thread::scope(|scope| {
        for t in 0..THREADS {
            let (s, locks) = (&s, &locks);
            scope.spawn(move || {
                for r in 0..ROUNDS {
                    locks
                        .with((7, "staging".to_string()), || {
                            s.step_gate(7, "staging", &run_request(t, r))
                        })
                        .expect("serialized round");
                }
            });
        }
    });
    let (cs, version) = s.load_gate(7, "staging").unwrap();
    let total = i64::try_from(THREADS * ROUNDS).unwrap();
    assert_eq!(cs.runs[RUN_KIND_PLAN].attempt, total);
    // The first request queues; every later one supersedes and queues.
    assert_eq!(version, 2 * total - 1);
    let (mut replayed, _) = s.replay::<Gate>(&exec_stream_id(7, "staging")).unwrap();
    (replayed.pr, replayed.environment) = (7, "staging".into());
    assert_eq!(replayed, cs);
}

/// Without the lock, a round that read a version another round moved past fails
/// with a conflict instead of writing over it: no event is lost or duplicated.
#[test]
fn unserialized_rounds_conflict_instead_of_losing_events() {
    let (_dir, s) = temp_store();
    let results: Vec<Result<GateStep>> = thread::scope(|scope| {
        let handles: Vec<_> = (0..THREADS)
            .map(|t| {
                let s = &s;
                scope.spawn(move || {
                    (0..ROUNDS)
                        .map(|r| s.step_gate(7, "staging", &run_request(t, r)))
                        .collect::<Vec<_>>()
                })
            })
            .collect();
        handles
            .into_iter()
            .flat_map(|h| h.join().unwrap())
            .collect()
    });
    let ok = results.iter().filter(|r| r.is_ok()).count();
    assert!(
        results
            .iter()
            .all(|r| matches!(r, Ok(_) | Err(Error::ConcurrencyConflict))),
        "{results:?}"
    );
    let (events, _) = s.load_events(&exec_stream_id(7, "staging")).unwrap();
    let queued = events.iter().filter(|e| e.kind == "RunQueued").count();
    assert_eq!(queued, ok);
}

/// Holding one key's lock does not block another key.
#[test]
fn keyed_locks_are_per_key() {
    let locks: KeyedLocks<&str> = KeyedLocks::default();
    let (tx, rx) = mpsc::channel();
    thread::scope(|scope| {
        locks.with("a", || {
            scope.spawn(|| locks.with("b", || tx.send(()).unwrap()));
            rx.recv_timeout(Duration::from_secs(5))
                .expect("key b ran while key a was held");
        });
    });
}

/// Gate rounds on one connection while claim rounds and raw writes hit the same
/// file through others: the immediate transactions wait out each other's locks.
#[test]
fn rounds_survive_a_busy_database() {
    let (dir, s) = temp_store();
    let path = dir.path().join("test.db");
    let other = Store::open(&path).unwrap();
    let noisy = Store::open(&path).unwrap();
    init(&s, "e1", 7, "staging", &[("stacks/a", "p1")]);
    let errors: Vec<String> = thread::scope(|scope| {
        let gates = scope.spawn(|| {
            for r in 0..20 {
                s.step_gate(7, "staging", &run_request(0, r))
                    .map_err(|e| format!("gate round {r}: {e}"))?;
            }
            Ok::<(), String>(())
        });
        let claims = scope.spawn(|| {
            for r in 0..20 {
                let cmd = AcquireClaim {
                    pr: 7,
                    stacks: vec![format!("stacks/{r}")],
                    now: Timestamp::now(),
                };
                other
                    .step_claims("staging", &cmd.into())
                    .map_err(|e| format!("claim round {r}: {e}"))?;
            }
            Ok(())
        });
        let noise = scope.spawn(|| {
            for r in 0..20 {
                noisy
                    .write(|tx| {
                        tx.execute(
                            "INSERT INTO execution_phases (execution_id, phase) VALUES ('noise', ?1)",
                            [r],
                        )?;
                        Ok(())
                    })
                    .map_err(|e| format!("noise {r}: {e}"))?;
            }
            Ok(())
        });
        [gates, claims, noise]
            .into_iter()
            .filter_map(|h| h.join().unwrap().err())
            .collect()
    });
    assert!(errors.is_empty(), "{errors:?}");
    assert_eq!(s.list_claims("staging").unwrap().len(), 20);
}
