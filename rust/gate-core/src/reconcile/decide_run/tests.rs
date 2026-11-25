// Ported case by case from internal/reconcile/run_test.go.
use std::collections::{BTreeMap, HashSet};

use crate::reconcile::react::tests::{render, sse};
use crate::reconcile::*;

fn state(runs: Vec<Run>) -> ChangeSet {
    ChangeSet {
        pr: 7,
        environment: "nonprod".into(),
        runs: runs
            .into_iter()
            .map(|r| (r.kind.clone(), r))
            .collect::<BTreeMap<_, _>>(),
        ..ChangeSet::default()
    }
}

fn run(id: &str, kind: &str, sha: &str, build_ref: &str, attempt: i64, phase: RunPhase) -> Run {
    Run {
        execution_id: id.into(),
        kind: kind.into(),
        sha: sha.into(),
        branch: String::new(),
        attempt,
        build_ref: build_ref.into(),
        phase,
    }
}

fn requested(kind: &str, sha: &str, branch: &str, rerun: bool) -> Signal {
    RunRequested {
        kind: kind.into(),
        sha: sha.into(),
        branch: branch.into(),
        rerun,
    }
    .into()
}

fn queued(kind: &str, sha: &str, branch: &str, id: &str, attempt: i64) -> Event {
    RunQueued {
        kind: kind.into(),
        sha: sha.into(),
        branch: branch.into(),
        execution_id: id.into(),
        attempt,
    }
    .into()
}

fn superseded(old: &str, old_ref: &str, new: &str, new_sha: &str) -> Event {
    RunSuperseded {
        kind: RUN_KIND_PLAN.into(),
        old_execution_id: old.into(),
        old_build_ref: old_ref.into(),
        new_execution_id: new.into(),
        new_sha: new_sha.into(),
    }
    .into()
}

fn fold(st: &ChangeSet, evs: &[Event]) -> ChangeSet {
    evs.iter().fold(st.clone(), evolve)
}

// --- decide: RunRequested ---

#[test]
fn decide_run_requested_queues_first_run() {
    let got = decide(
        &state(vec![]),
        &requested(RUN_KIND_PLAN, "abcdef1234567890", "feat/x", false),
    );
    assert_eq!(
        got,
        vec![queued(
            RUN_KIND_PLAN,
            "abcdef1234567890",
            "feat/x",
            "run-7-nonprod-plan-abcdef123456-a1",
            1
        )]
    );
}

#[test]
fn decide_run_requested_redelivery_no_ops() {
    let st = state(vec![run(
        "e1",
        RUN_KIND_PLAN,
        "sha1",
        "",
        1,
        RunPhase::Started,
    )]);
    assert!(decide(&st, &requested(RUN_KIND_PLAN, "sha1", "", false)).is_empty());
}

#[test]
fn decide_run_requested_new_sha_supersedes_live_plan() {
    let st = state(vec![run(
        "e-old",
        RUN_KIND_PLAN,
        "oldsha",
        "b-1",
        1,
        RunPhase::Started,
    )]);
    let got = decide(&st, &requested(RUN_KIND_PLAN, "newsha", "feat/x", false));
    // Attempt 2: attempts grow across requests, so a force-push back to an old SHA
    // never re-mints its dead execution id.
    assert_eq!(
        got,
        vec![
            superseded("e-old", "b-1", "run-7-nonprod-plan-newsha-a2", "newsha"),
            queued(
                RUN_KIND_PLAN,
                "newsha",
                "feat/x",
                "run-7-nonprod-plan-newsha-a2",
                2
            ),
        ]
    );
}

#[test]
fn decide_run_requested_never_disturbs_live_apply() {
    let st = state(vec![run(
        "e-apply",
        RUN_KIND_APPLY,
        "mergesha",
        "",
        1,
        RunPhase::Started,
    )]);
    assert!(decide(&st, &requested(RUN_KIND_APPLY, "newermerge", "", false)).is_empty());
    assert!(decide(&st, &requested(RUN_KIND_APPLY, "mergesha", "", true)).is_empty());
}

#[test]
fn decide_run_requested_rerun_supersedes_and_bumps_attempt() {
    let st = state(vec![run(
        "e1",
        RUN_KIND_PLAN,
        "sha1",
        "b-1",
        1,
        RunPhase::Started,
    )]);
    let got = decide(&st, &requested(RUN_KIND_PLAN, "sha1", "feat/x", true));
    assert_eq!(
        got,
        vec![
            superseded("e1", "b-1", "run-7-nonprod-plan-sha1-a2", "sha1"),
            queued(
                RUN_KIND_PLAN,
                "sha1",
                "feat/x",
                "run-7-nonprod-plan-sha1-a2",
                2
            ),
        ]
    );
}

#[test]
fn decide_run_requested_retry_after_start_failure() {
    let st = state(vec![run(
        "e1",
        RUN_KIND_APPLY,
        "sha1",
        "",
        1,
        RunPhase::StartFailed,
    )]);
    let got = decide(&st, &requested(RUN_KIND_APPLY, "sha1", "", true));
    // Nothing live to cancel: re-queue without a supersede.
    assert_eq!(
        got,
        vec![queued(
            RUN_KIND_APPLY,
            "sha1",
            "",
            "run-7-nonprod-apply-sha1-a2",
            2
        )]
    );
}

#[test]
fn decide_run_requested_rejects_garbage() {
    let st = state(vec![]);
    assert!(decide(&st, &requested("deploy", "x", "", false)).is_empty());
    assert!(decide(&st, &requested(RUN_KIND_PLAN, "", "", false)).is_empty());
}

// --- decide: run completion ---

#[test]
fn decide_finalize_completes_run() {
    let st = state(vec![run(
        "e1",
        RUN_KIND_PLAN,
        "sha1",
        "b-1",
        1,
        RunPhase::Started,
    )]);
    let evs = decide(&st, &RunnerFinalize::default().into());
    let completed = events_of!(evs, RunCompleted);
    assert_eq!(
        completed,
        vec![RunCompleted {
            kind: RUN_KIND_PLAN.into(),
            execution_id: "e1".into()
        }],
        "{evs:?}"
    );
    // Folded, the run is no longer live, so a new SHA queues without a cancel.
    let post = fold(&st, &evs);
    let got = decide(&post, &requested(RUN_KIND_PLAN, "sha2", "", false));
    assert_eq!(got.len(), 1, "{got:?}");
    assert!(matches!(got[0], Event::RunQueued(_)), "{got:?}");
}

#[test]
fn decide_apply_finalize_completes_apply_run() {
    // A finished apply used to stay live, so every later apply request was dropped.
    let st = state(vec![run(
        "e-apply",
        RUN_KIND_APPLY,
        "mergesha",
        "",
        1,
        RunPhase::Started,
    )]);
    let evs = decide(
        &st,
        &RunnerFinalize {
            gates: vec![],
            apply_context: true,
        }
        .into(),
    );
    let post = fold(&st, &evs);
    assert!(!post.runs[RUN_KIND_APPLY].live(), "{:?}", post.runs);
    let rerun = decide(&post, &requested(RUN_KIND_APPLY, "mergesha", "", true));
    assert_eq!(rerun.len(), 1, "{rerun:?}");
    let q = last_of!(rerun, RunQueued).expect("RunQueued");
    assert_eq!(q.attempt, 2);
}

#[test]
fn decide_finalize_completes_start_failed_run() {
    let st = state(vec![run(
        "e1",
        RUN_KIND_PLAN,
        "sha1",
        "",
        1,
        RunPhase::StartFailed,
    )]);
    let evs = decide(&st, &RunnerFinalize::default().into());
    let post = fold(&st, &evs);
    assert_eq!(post.runs[RUN_KIND_PLAN].phase, RunPhase::Completed);
}

// --- decide: redelivery and attempts ---

#[test]
fn decide_run_requested_redelivery_after_completion_no_ops() {
    let st = state(vec![run(
        "e1",
        RUN_KIND_APPLY,
        "mergesha",
        "",
        1,
        RunPhase::Completed,
    )]);
    assert!(decide(&st, &requested(RUN_KIND_APPLY, "mergesha", "", false)).is_empty());
}

#[test]
fn decide_run_requested_force_push_round_trip_mints_fresh_ids() {
    let mut st = state(vec![]);
    let mut ids = HashSet::new();
    for sha in ["shaA", "shaB", "shaA"] {
        let evs = decide(&st, &requested(RUN_KIND_PLAN, sha, "", false));
        for q in events_of!(evs, RunQueued) {
            assert!(
                ids.insert(q.execution_id.clone()),
                "{} minted twice",
                q.execution_id
            );
        }
        st = fold(&st, &evs);
    }
    assert_eq!(ids.len(), 3, "{ids:?}");
}

// --- decide: RunStartResult ---

fn start_result(kind: &str, id: &str, build_ref: &str, err: &str) -> Signal {
    RunStartResult {
        kind: kind.into(),
        execution_id: id.into(),
        build_ref: build_ref.into(),
        err: err.into(),
    }
    .into()
}

#[test]
fn decide_run_start_result_records_start() {
    let st = state(vec![run(
        "e1",
        RUN_KIND_PLAN,
        "sha1",
        "",
        0,
        RunPhase::Queued,
    )]);
    let got = decide(&st, &start_result(RUN_KIND_PLAN, "e1", "b-42", ""));
    let want: Vec<Event> = vec![
        RunStarted {
            kind: RUN_KIND_PLAN.into(),
            execution_id: "e1".into(),
            build_ref: "b-42".into(),
        }
        .into(),
    ];
    assert_eq!(got, want);
}

#[test]
fn decide_run_start_result_records_failure() {
    let st = state(vec![run(
        "e1",
        RUN_KIND_PLAN,
        "sha1",
        "",
        0,
        RunPhase::Queued,
    )]);
    let got = decide(
        &st,
        &start_result(RUN_KIND_PLAN, "e1", "", "trigger not found"),
    );
    let want: Vec<Event> = vec![
        RunStartFailed {
            kind: RUN_KIND_PLAN.into(),
            execution_id: "e1".into(),
            reason: "trigger not found".into(),
        }
        .into(),
    ];
    assert_eq!(got, want);
}

#[test]
fn decide_run_start_result_drops_stale_feedback() {
    let st = state(vec![run(
        "e-new",
        RUN_KIND_PLAN,
        "sha2",
        "",
        0,
        RunPhase::Queued,
    )]);
    assert!(decide(&st, &start_result(RUN_KIND_PLAN, "e-old", "b-1", "")).is_empty());
    assert!(decide(&st, &start_result(RUN_KIND_APPLY, "e-x", "", "")).is_empty());
}

// --- evolve ---

#[test]
fn evolve_run_queued_stores_run() {
    let got = evolve(
        ChangeSet::default(),
        &queued(RUN_KIND_PLAN, "sha1", "b", "e1", 1),
    );
    let want = Run {
        branch: "b".into(),
        ..run("e1", RUN_KIND_PLAN, "sha1", "", 1, RunPhase::Queued)
    };
    assert_eq!(got.runs[RUN_KIND_PLAN], want);
}

#[test]
fn evolve_run_started_sets_build_ref() {
    let st = evolve(
        ChangeSet::default(),
        &queued(RUN_KIND_PLAN, "sha1", "", "e1", 1),
    );
    let got = evolve(
        st,
        &RunStarted {
            kind: RUN_KIND_PLAN.into(),
            execution_id: "e1".into(),
            build_ref: "b-42".into(),
        }
        .into(),
    );
    let r = &got.runs[RUN_KIND_PLAN];
    assert_eq!((r.phase, r.build_ref.as_str()), (RunPhase::Started, "b-42"));
    // A stale start for a replaced execution does not fold.
    let got2 = evolve(
        got,
        &RunStarted {
            kind: RUN_KIND_PLAN.into(),
            execution_id: "e-old".into(),
            build_ref: "b-9".into(),
        }
        .into(),
    );
    assert_eq!(got2.runs[RUN_KIND_PLAN].build_ref, "b-42");
}

#[test]
fn evolve_run_superseded_then_queued_replaces() {
    let st = evolve(
        ChangeSet::default(),
        &queued(RUN_KIND_PLAN, "sha1", "", "e1", 1),
    );
    let st = evolve(st, &superseded("e1", "", "e2", "sha2"));
    assert_eq!(st.runs[RUN_KIND_PLAN].phase, RunPhase::Superseded);
    let st = evolve(st, &queued(RUN_KIND_PLAN, "sha2", "", "e2", 1));
    let r = &st.runs[RUN_KIND_PLAN];
    assert_eq!((r.execution_id.as_str(), r.phase), ("e2", RunPhase::Queued));
}

#[test]
fn evolve_run_clones_map() {
    // Go clones the runs map so a fold never mutates a prior state; in Rust the
    // fold takes the state by value, and a clone of the prior stays untouched.
    let prior = evolve(
        ChangeSet::default(),
        &queued(RUN_KIND_PLAN, "sha1", "", "e1", 1),
    );
    let _ = evolve(
        prior.clone(),
        &RunStartFailed {
            kind: RUN_KIND_PLAN.into(),
            execution_id: "e1".into(),
            reason: "x".into(),
        }
        .into(),
    );
    assert_eq!(prior.runs[RUN_KIND_PLAN].phase, RunPhase::Queued);
}

// --- react ---

#[test]
fn react_run_queued_starts_run() {
    let got = react(
        &ChangeSet::default(),
        &[queued(RUN_KIND_PLAN, "sha1", "feat/x", "e1", 1)],
    );
    let want: Vec<Action> = vec![
        StartRun {
            kind: RUN_KIND_PLAN.into(),
            sha: "sha1".into(),
            branch: "feat/x".into(),
            execution_id: "e1".into(),
        }
        .into(),
        render(false, ""),
        sse(),
    ];
    assert_eq!(got, want);
}

#[test]
fn react_run_superseded_cancels_then_starts() {
    let evs = [
        superseded("e1", "b-1", "e2", "sha2"),
        queued(RUN_KIND_PLAN, "sha2", "feat/x", "e2", 1),
    ];
    let got = react(&ChangeSet::default(), &evs);
    let want: Vec<Action> = vec![
        CancelRun {
            kind: RUN_KIND_PLAN.into(),
            old_execution_id: "e1".into(),
            old_build_ref: "b-1".into(),
            new_execution_id: "e2".into(),
        }
        .into(),
        StartRun {
            kind: RUN_KIND_PLAN.into(),
            sha: "sha2".into(),
            branch: "feat/x".into(),
            execution_id: "e2".into(),
        }
        .into(),
        render(false, ""),
        sse(),
    ];
    assert_eq!(got, want);
}

#[test]
fn react_run_start_failed_renders_failure() {
    let evs = [RunStartFailed {
        kind: RUN_KIND_PLAN.into(),
        execution_id: "e1".into(),
        reason: "api error".into(),
    }
    .into()];
    assert_eq!(
        react(&ChangeSet::default(), &evs),
        vec![render(true, "failure"), sse()]
    );
}

#[test]
fn react_run_started_renders_in_progress() {
    let evs = [RunStarted {
        kind: RUN_KIND_PLAN.into(),
        execution_id: "e1".into(),
        build_ref: "b-1".into(),
    }
    .into()];
    assert_eq!(
        react(&ChangeSet::default(), &evs),
        vec![render(false, ""), sse()]
    );
}
