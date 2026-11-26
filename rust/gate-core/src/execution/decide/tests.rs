// Ported case by case from internal/execution/decide_test.go.
use std::collections::BTreeMap;

use crate::events::{Phase, Status};
use crate::execution::*;

#[test]
fn decide_report_init_emits_started() {
    let exec = State {
        id: "e1".into(),
        repo: "r".into(),
        sha: "abc".into(),
        stacks: vec![Stack {
            path: "a".into(),
            ..Stack::default()
        }],
        ..State::default()
    };
    let got = decide(&State::default(), &ReportInit { exec: exec.clone() }.into());
    assert_eq!(got, vec![Started { exec }.into()]);
}

#[test]
fn decide_report_phase_emits_phase_changed() {
    let got = decide(
        &State::default(),
        &ReportPhase {
            phase: Phase::new(Phase::APPLYING),
            ..ReportPhase::default()
        }
        .into(),
    );
    assert_eq!(
        got,
        vec![
            PhaseChanged {
                phase: Phase::new(Phase::APPLYING),
                ..PhaseChanged::default()
            }
            .into()
        ]
    );
}

#[test]
fn decide_report_tick_emits_stack_status_changed() {
    let got = decide(
        &State::default(),
        &ReportTick {
            stack: "a".into(),
            status: Status::Running,
            detail: "d".into(),
        }
        .into(),
    );
    assert_eq!(
        got,
        vec![
            StackStatusChanged {
                stack: "a".into(),
                status: Status::Running,
                detail: "d".into()
            }
            .into()
        ]
    );
}

#[test]
fn decide_report_fail_emits_failed() {
    assert_eq!(
        decide(&State::default(), &ReportFail {}.into()),
        vec![Failed {}.into()]
    );
}

#[test]
fn decide_report_succeed_emits_succeeded() {
    assert_eq!(
        decide(&State::default(), &ReportSucceed {}.into()),
        vec![Succeeded {}.into()]
    );
}

#[test]
fn decide_report_annotate_emits_stacks_annotated() {
    let got = decide(
        &State::default(),
        &ReportAnnotate {
            projects: BTreeMap::from([("a".into(), "p".into())]),
            ..ReportAnnotate::default()
        }
        .into(),
    );
    assert_eq!(got.len(), 1);
    assert!(matches!(got[0], Event::StacksAnnotated(_)), "{got:?}");
}

#[test]
fn decide_report_supersede_emits_superseded() {
    let s = State {
        id: "e1".into(),
        ..State::default()
    };
    let got = decide(
        &s,
        &ReportSupersede {
            by: "new-exec".into(),
        }
        .into(),
    );
    assert_eq!(
        got,
        vec![
            Superseded {
                by: "new-exec".into()
            }
            .into()
        ]
    );
}

#[test]
fn decide_report_supersede_noop_on_empty_state() {
    let sig: Signal = ReportSupersede { by: "x".into() }.into();
    assert!(decide(&State::default(), &sig).is_empty());
    let s = State {
        id: "e1".into(),
        ..State::default()
    };
    assert_eq!(decide(&s, &sig).len(), 1);
}
