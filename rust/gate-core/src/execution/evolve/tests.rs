// Ported case by case from internal/execution/evolve_test.go.
use std::collections::BTreeMap;

use crate::events::{Category, Counts, Phase, Status};
use crate::execution::*;

fn stack(path: &str, status: Status) -> Stack {
    Stack {
        path: path.into(),
        run_status: status,
        ..Stack::default()
    }
}

fn exec(id: &str, stacks: Vec<Stack>) -> State {
    State {
        id: id.into(),
        stacks,
        ..State::default()
    }
}

fn started(exec: State) -> Event {
    Started { exec }.into()
}

fn phase(p: &str) -> Event {
    PhaseChanged {
        phase: Phase::new(p),
        ..PhaseChanged::default()
    }
    .into()
}

fn tick(stack: &str, status: Status, detail: &str) -> Event {
    StackStatusChanged {
        stack: stack.into(),
        status,
        detail: detail.into(),
    }
    .into()
}

#[test]
fn evolve_started_replaces_state() {
    let prior = State {
        id: "old".into(),
        phase: Phase::new(Phase::APPLYING),
        ..State::default()
    };
    let got = evolve(prior, &started(exec("e1", vec![stack("a", Status::Unset)])));
    assert_eq!(got.id, "e1");
    assert_eq!(got.stacks.len(), 1);
    assert_eq!(got.phase, Phase::default());
}

#[test]
fn evolve_phase_changed() {
    let got = evolve(exec("e1", vec![]), &phase(Phase::APPLYING));
    assert_eq!(got.phase.as_str(), Phase::APPLYING);
    assert_eq!(got.id, "e1");
}

#[test]
fn evolve_stack_status_changed() {
    let prior = exec(
        "",
        vec![stack("a", Status::Unset), stack("b", Status::Unset)],
    );
    let got = evolve(prior, &tick("b", Status::Failed, "boom"));
    assert_eq!(got.stacks[0].run_status, Status::Unset);
    assert_eq!(got.stacks[1].run_status, Status::Failed);
    assert_eq!(got.stacks[1].detail, "boom");
}

#[test]
fn evolve_failed_aborts_innocent_stacks() {
    let prior = exec(
        "",
        vec![
            stack("pend", Status::Pending),
            stack("run", Status::Running),
            stack("init", Status::Initializing),
            stack("inited", Status::Initialized),
            stack("moving", Status::Moving),
            stack("done", Status::Planned),
            stack("bad", Status::Failed),
        ],
    );
    let got = evolve(prior, &Failed {}.into());
    let want = BTreeMap::from([
        ("pend", Status::Aborted),
        ("run", Status::Aborted),
        ("init", Status::Aborted),
        ("inited", Status::Aborted),
        ("moving", Status::Aborted), // moving is innocent: aborted
        ("done", Status::Planned),   // terminal statuses are untouched
        ("bad", Status::Failed),
    ]);
    for s in &got.stacks {
        assert_eq!(s.run_status, want[s.path.as_str()], "stack {}", s.path);
    }
}

#[test]
fn evolve_fold_sequence() {
    let s = [
        started(exec("e1", vec![stack("a", Status::Unset)])),
        phase(Phase::APPLYING),
        tick("a", Status::Running, ""),
        Failed {}.into(),
    ]
    .iter()
    .fold(State::default(), evolve);
    let want = State {
        id: "e1".into(),
        phase: Phase::new(Phase::APPLYING),
        status: "failure".into(),
        stacks: vec![stack("a", Status::Aborted)],
        ..State::default()
    };
    assert_eq!(s, want);
}

#[test]
fn evolve_started_carries_identity_and_edges() {
    let got = evolve(
        State::default(),
        &started(State {
            id: "e1".into(),
            pr: 7,
            environment: "nonprod".into(),
            context: "terraform/nonprod".into(),
            status: "in_progress".into(),
            stacks: vec![stack("a", Status::Unset)],
            edges: vec![Edge {
                from: "a".into(),
                to: "b".into(),
            }],
            ..State::default()
        }),
    );
    assert_eq!(
        (got.pr, got.environment.as_str(), got.context.as_str()),
        (7, "nonprod", "terraform/nonprod")
    );
    assert_eq!(got.status, "in_progress");
    assert_eq!(got.edges.len(), 1);
}

#[test]
fn evolve_phase_changed_folds_progress() {
    let got = evolve(
        exec("e1", vec![]),
        &PhaseChanged {
            phase: Phase::new(Phase::APPLYING),
            label: "applying 3/8".into(),
            pct: Some(42),
            ..PhaseChanged::default()
        }
        .into(),
    );
    assert_eq!(got.phase.as_str(), Phase::APPLYING);
    assert_eq!(got.progress_label, "applying 3/8");
    assert_eq!(got.progress_pct, Some(42));
}

#[test]
fn evolve_phase_changed_sets_identity_non_regressively() {
    // Phase before init: the identity materializes the row.
    let got = evolve(
        State::default(),
        &PhaseChanged {
            phase: Phase::new(Phase::INITIALIZING),
            id: "e9".into(),
            pr: 9,
            environment: "prod".into(),
            context: "terraform/prod".into(),
            repo: "r".into(),
            sha: "abc".into(),
            ..PhaseChanged::default()
        }
        .into(),
    );
    assert_eq!(
        (got.pr, got.environment.as_str(), got.repo.as_str()),
        (9, "prod", "r")
    );
    // A later phase with an empty identity does not clobber it.
    let got2 = evolve(got, &phase(Phase::APPLYING));
    assert_eq!(
        (got2.pr, got2.environment.as_str(), got2.repo.as_str()),
        (9, "prod", "r")
    );
}

#[test]
fn evolve_failed_sets_run_status() {
    let got = evolve(
        State {
            status: "in_progress".into(),
            stacks: vec![stack("a", Status::Running)],
            ..State::default()
        },
        &Failed {}.into(),
    );
    assert_eq!(got.status, "failure");
    assert_eq!(got.stacks[0].run_status, Status::Aborted);
}

#[test]
fn evolve_succeeded_sets_run_status() {
    let got = evolve(
        State {
            status: "in_progress".into(),
            ..State::default()
        },
        &Succeeded {}.into(),
    );
    assert_eq!(got.status, "success");
}

#[test]
fn evolve_stacks_annotated() {
    let prior = exec(
        "",
        vec![
            stack("a", Status::Planned),
            stack("b", Status::Planned),
            stack("c", Status::Failed),
        ],
    );
    let got = evolve(
        prior,
        &StacksAnnotated {
            projects: BTreeMap::from([("a".into(), "proj-a".into())]),
            categories: BTreeMap::from([(
                "a".into(),
                vec![Category {
                    name: "iam".into(),
                    ..Category::default()
                }],
            )]),
            counts: BTreeMap::from([("a".into(), Counts::default())]),
            moving: vec!["b".into(), "c".into()], // c failed: must not become moving
        }
        .into(),
    );
    assert_eq!(got.stacks[0].project, "proj-a");
    assert_eq!(got.stacks[0].categories.len(), 1);
    assert!(got.stacks[0].counts.is_some());
    assert_eq!(got.stacks[1].run_status, Status::Moving);
    assert_eq!(got.stacks[2].run_status, Status::Failed);
}

#[test]
fn evolve_superseded_sets_superseded_by() {
    let got = evolve(
        exec("old", vec![]),
        &Superseded {
            by: "new-exec".into(),
        }
        .into(),
    );
    assert_eq!(got.superseded_by, "new-exec");
}

#[test]
fn evolve_started_clears_superseded_by() {
    // A fresh Init un-supersedes a revived execution id.
    let prior = evolve(exec("e1", vec![]), &Superseded { by: "n1".into() }.into());
    assert_eq!(prior.superseded_by, "n1");
    let got = evolve(prior, &started(exec("e1", vec![stack("a", Status::Unset)])));
    assert_eq!(got.superseded_by, "");
}

#[test]
fn evolve_started_is_non_regressive() {
    // A repeat Init (`run register`, then `run plan`) does not regress an advanced
    // stack's progress.
    let s = evolve(
        State::default(),
        &started(exec(
            "e1",
            vec![
                stack("a", Status::Pending),
                stack("b", Status::Pending),
                stack("c", Status::Pending),
            ],
        )),
    );
    let s = evolve(s, &tick("a", Status::Initialized, ""));

    // The second Init omits c and refreshes a's project; all reported pending.
    let got = evolve(
        s,
        &started(exec(
            "e1",
            vec![
                Stack {
                    project: "proj-a".into(),
                    ..stack("a", Status::Pending)
                },
                stack("b", Status::Pending),
            ],
        )),
    );

    let by_path: BTreeMap<&str, &Stack> =
        got.stacks.iter().map(|st| (st.path.as_str(), st)).collect();
    assert_eq!(by_path["a"].run_status, Status::Initialized);
    assert_eq!(by_path["a"].project, "proj-a");
    assert_eq!(by_path["b"].run_status, Status::Pending);
    assert_eq!(by_path["c"].run_status, Status::Pending);
    // Init stacks first, then carried-forward prior-only stacks.
    let order: Vec<&str> = got.stacks.iter().map(|st| st.path.as_str()).collect();
    assert_eq!(order, ["a", "b", "c"]);
}

#[test]
fn evolve_started_does_not_mutate_input_event() {
    // Evolve borrows the event, so the compiler rules out writing through it; the
    // test stays to match Go's.
    let prior = evolve(
        evolve(
            State::default(),
            &started(exec("", vec![stack("a", Status::Pending)])),
        ),
        &tick("a", Status::Initialized, ""),
    );
    let ev2 = started(exec("", vec![stack("a", Status::Pending)]));
    let _ = evolve(prior, &ev2);
    let Event::Started(s) = &ev2 else {
        unreachable!("built as Started")
    };
    assert_eq!(s.exec.stacks[0].run_status, Status::Pending);
}
