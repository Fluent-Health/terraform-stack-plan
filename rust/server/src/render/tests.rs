// Ported from internal/server/render_test.go, status_test.go (the pure conclusion
// tests; TestLoadSnapshot is in checkrun/tests.rs), triage_test.go and
// progress_weighted_test.go.
use super::*;

fn st(path: &str, status: Status) -> StackState {
    StackState {
        path: path.into(),
        status,
        ..StackState::default()
    }
}

fn with_counts(mut s: StackState, c: Counts) -> StackState {
    s.counts = Some(c);
    s
}

fn with_cats(mut s: StackState, names: &[&str]) -> StackState {
    s.categories = names
        .iter()
        .map(|n| Category {
            name: (*n).into(),
            ..Category::default()
        })
        .collect();
    s
}

fn failed(path: &str, detail: &str) -> StackState {
    StackState {
        detail: detail.into(),
        ..st(path, Status::Failed)
    }
}

fn graph(stacks: Vec<StackState>) -> Graph {
    Graph {
        stacks,
        ..Graph::default()
    }
}

fn phase(p: &str) -> Phase {
    Phase::new(p)
}

fn assert_contains(out: &str, wants: &[&str]) {
    for w in wants {
        assert!(out.contains(w), "missing {w:?} in:\n{out}");
    }
}

fn no_headline(out: &str) {
    assert!(
        !out.starts_with("## "),
        "summary must not start with a ## headline; got:\n{out}"
    );
}

// --- render_test.go ---

#[test]
fn failures_section_basic() {
    let none = graph(vec![st("a", Status::Planned)]);
    assert_eq!(
        failures_section(&none, "", ""),
        "",
        "no failures should render empty"
    );
    let g = graph(vec![failed("stacks/x", "boom")]);
    let out = failures_section(&g, "https://ci/log", "");
    assert_contains(
        &out,
        &["Failures (1)", "stacks/x", "boom", "https://ci/log"],
    );
}

#[test]
fn failures_section_per_stack_log_link() {
    let g = graph(vec![failed("cluster/fh-prod", "terraform apply failed")]);
    let out = failures_section(&g, "https://ci/log", "https://serve/logs/apply-1");
    assert_contains(
        &out,
        &[
            "cluster/fh-prod",
            "terraform apply failed",
            "https://serve/logs/apply-1/cluster/fh-prod",
        ],
    );
}

#[test]
fn failures_section_triage_iam_denied() {
    let g = graph(vec![failed(
        "stacks/prod",
        "Error: Error 403: Permission 'run.services.setIamPolicy' denied",
    )]);
    let out = failures_section(&g, "", "");
    assert_contains(
        &out,
        &[
            "Error 403",
            "Likely cause",
            "Next steps",
            "Re-request elevated access",
            "Safe to retry",
        ],
    );
}

#[test]
fn failures_section_triage_unmatched_no_fabricated_cause() {
    let g = graph(vec![failed("stacks/dev", "panic: something totally novel")]);
    let out = failures_section(&g, "", "");
    assert!(out.contains("panic: something totally novel"), "{out}");
    assert!(!out.contains("Likely cause"), "fabricated cause in:\n{out}");
    assert!(out.contains("Next steps"), "{out}");
}

#[test]
fn failures_section_triage_no_detail_suppresses_triage() {
    let g = graph(vec![st("stacks/qa", Status::Failed)]);
    let out = failures_section(&g, "", "");
    assert!(out.contains("No error detail captured"), "{out}");
    for unwanted in ["Likely cause", "Next steps", "State impact"] {
        assert!(
            !out.contains(unwanted),
            "{unwanted:?} must not render in:\n{out}"
        );
    }
}

#[test]
fn pam_console_url_points_at_approval_tab() {
    assert_eq!(
        pam_console_url("fh-dev-svc"),
        "https://console.cloud.google.com/iam-admin/pam/grants/approvals?project=fh-dev-svc"
    );
}

#[test]
fn check_summary_plan_finalized() {
    let stacks = vec![
        with_cats(
            with_counts(
                st("svc/a", Status::Planned),
                Counts {
                    add: 6,
                    ..Counts::default()
                },
            ),
            &["iam"],
        ),
        with_cats(
            with_counts(
                st("svc/b", Status::Planned),
                Counts {
                    destroy: 2,
                    ..Counts::default()
                },
            ),
            &["destructive"],
        ),
        with_counts(
            st("svc/c", Status::Planned),
            Counts {
                change: 3,
                ..Counts::default()
            },
        ),
    ];
    let out = check_summary(
        "plan",
        &stacks,
        "https://srv/live/e1",
        &phase(Phase::PLANNING),
    );
    no_headline(&out);
    assert_contains(
        &out,
        &[
            "⚠️ destructive",
            "⚿ 1 IAM",
            "[live viewer ↗](https://srv/live/e1)",
            "| Stack | Ops | Risk | State |",
            "`svc/a`",
            "`svc/b`",
            "`svc/c`",
            "planned",
        ],
    );
}

#[test]
fn check_summary_plan_degrades_while_planning() {
    let stacks = vec![st("svc/a", Status::Planned), st("svc/b", Status::Running)];
    let out = check_summary(
        "plan",
        &stacks,
        "https://srv/live/e1",
        &phase(Phase::PLANNING),
    );
    no_headline(&out);
    assert!(!out.contains("⚠️ destructive"), "{out}");
    assert!(
        out.contains("`svc/a`") && out.contains("| Stack | Ops | Risk | State |"),
        "{out}"
    );
}

#[test]
fn check_summary_apply_applied() {
    let stacks = vec![
        with_counts(
            st("svc/a", Status::Safe),
            Counts {
                add: 6,
                ..Counts::default()
            },
        ),
        with_counts(
            st("svc/b", Status::Safe),
            Counts {
                destroy: 2,
                ..Counts::default()
            },
        ),
    ];
    let out = check_summary(
        "apply",
        &stacks,
        "https://srv/live/e1",
        &phase(Phase::APPLYING),
    );
    no_headline(&out);
    assert_contains(
        &out,
        &["+6", "−2", "applied", "| Stack | Ops | Risk | State |"],
    );
}

#[test]
fn check_summary_apply_partial_failed() {
    let stacks = vec![
        with_counts(
            st("svc/a", Status::Safe),
            Counts {
                add: 6,
                ..Counts::default()
            },
        ),
        st("svc/b", Status::Failed),
    ];
    let out = check_summary(
        "apply",
        &stacks,
        "https://srv/live/e1",
        &phase(Phase::APPLYING),
    );
    no_headline(&out);
    assert_contains(&out, &["failed", "| Stack | Ops | Risk | State |"]);
}

#[test]
fn check_summary_no_env_no_chips() {
    let stacks = vec![with_counts(
        st("svc/a", Status::Planned),
        Counts {
            change: 1,
            ..Counts::default()
        },
    )];
    let out = check_summary(
        "plan",
        &stacks,
        "https://srv/live/e1",
        &phase(Phase::PLANNING),
    );
    no_headline(&out);
    assert!(
        !out.contains("⚠️ destructive") && !out.contains("IAM"),
        "{out}"
    );
}

#[test]
fn check_summary_planning_headline_has_bar() {
    let stacks = vec![st("svc/a", Status::Planned), st("svc/b", Status::Running)];
    let out = check_summary("plan", &stacks, "", &phase(Phase::PLANNING));
    no_headline(&out);
    assert!(out.contains("| Stack | Ops | Risk | State |"), "{out}");
}

#[test]
fn check_summary_warming_no_stacks() {
    let out = check_summary("plan", &[], "", &phase(Phase::PLANNING));
    no_headline(&out);
    assert!(!out.contains("| Stack | Ops | Risk | State |"), "{out}");
}

#[test]
fn check_summary_no_headline() {
    let stacks = vec![with_counts(
        st("a", Status::Failed),
        Counts {
            change: 2,
            ..Counts::default()
        },
    )];
    let out = check_summary("apply", &stacks, "http://viewer", &phase(Phase::APPLYING));
    assert!(
        !out.starts_with("## ") && !out.contains("## Apply"),
        "{out}"
    );
    assert!(out.contains("| Stack | Ops | Risk | State |"), "{out}");
}

#[test]
fn progress_cases() {
    let cases: &[(&str, &str, usize, usize, usize, &str, &str)] = &[
        (
            "warming",
            Phase::WARMING,
            0,
            0,
            0,
            "⠇⠀⠀⠀⠀⠀⠀⠀⠀⠀",
            "warming cache…",
        ),
        (
            "initializing",
            Phase::INITIALIZING,
            0,
            0,
            12,
            "⠿⠇⠀⠀⠀⠀⠀⠀⠀⠀",
            "initializing 12 stacks…",
        ),
        (
            "planning_mid",
            Phase::PLANNING,
            5,
            0,
            10,
            "⠿⠿⠿⠿⠿⠿⠀⠀⠀⠀",
            "planning 5/10",
        ),
        (
            "planning_done",
            Phase::PLANNING,
            10,
            0,
            10,
            "⠿⠿⠿⠿⠿⠿⠿⠿⠿⠿",
            "planned",
        ),
        (
            "planning_zero",
            Phase::PLANNING,
            0,
            0,
            0,
            "⠿⠿⠀⠀⠀⠀⠀⠀⠀⠀",
            "planning…",
        ),
        (
            "applying_mid",
            Phase::APPLYING,
            3,
            0,
            6,
            "⠿⠿⠿⠿⠿⠀⠀⠀⠀⠀",
            "applying 3/6",
        ),
        (
            "applying_done",
            Phase::APPLYING,
            6,
            0,
            6,
            "⠿⠿⠿⠿⠿⠿⠿⠿⠿⠿",
            "applied 6/6",
        ),
    ];
    for (name, p, planned, initialized, total, want_bar, want_label) in cases {
        let (bar, label, _) = progress(&[], &phase(p), *planned, *initialized, *total);
        assert_eq!(bar, *want_bar, "{name}: bar");
        assert_eq!(label, *want_label, "{name}: label");
        assert_eq!(bar.chars().count(), 10, "{name}: bar width");
    }
}

#[test]
fn progress_initialized() {
    let (_, label, _) = progress(&[], &phase(Phase::INITIALIZING), 0, 3, 10);
    assert_eq!(label, "initialized 3/10");
    let (_, label0, _) = progress(&[], &phase(Phase::INITIALIZING), 0, 0, 10);
    assert_eq!(label0, "initializing 10 stacks…");
}

#[test]
fn done_includes_new_terminal_states() {
    for s in [Status::Aborted, Status::Nochange] {
        assert!(done(s), "done({s}) = false, want true");
    }
}

fn target(class: &str, target: &str, state: &str) -> GateTargetRow {
    GateTargetRow {
        class: class.into(),
        target: target.into(),
        state: state.into(),
        ..GateTargetRow::default()
    }
}

#[test]
fn gates_section_cases() {
    assert_eq!(gates_section(&[]), "", "no targets → no banner");
    assert_eq!(
        gates_section(&[target("iam", "p", "ACTIVE")]),
        "",
        "all-active → no banner"
    );
    let s = gates_section(&[
        target("iam", "fh-dev-svc", "AWAITING"),
        target("iam", "fh-stage-svc", "ACTIVE"),
    ]);
    assert_contains(
        &s,
        &[
            "Awaiting approval",
            "fh-dev-svc",
            &pam_console_url("fh-dev-svc"),
        ],
    );
    assert!(!s.contains("fh-stage-svc"), "active gate in banner: {s}");
}

#[test]
fn error_tail_box_block() {
    let input = "Initializing...\nApplying...\n╷\n│ Error: creating Bucket: 403\n│\n│   on storage.tf line 8\n╵\n";
    let got = error_tail(input, 25);
    assert_contains(
        &got,
        &["Error: creating Bucket: 403", "on storage.tf line 8"],
    );
    assert!(!got.contains("Initializing..."), "{got}");
}

#[test]
fn error_tail_error_line_no_box() {
    let got = error_tail(
        "step 1 ok\nstep 2 ok\nError: terraform exited with code 1\n",
        25,
    );
    assert_eq!(got, "Error: terraform exited with code 1");
}

#[test]
fn error_tail_fallback_last_lines() {
    assert_eq!(error_tail("a\nb\nc\nd\n", 2), "c\nd");
}

#[test]
fn error_tail_empty() {
    assert_eq!(error_tail("   \n\n", 25), "");
}

#[test]
fn error_tail_cap() {
    assert_eq!(error_tail("l1\nl2\nl3\nl4\nl5\n", 3), "l3\nl4\nl5");
}

#[test]
fn error_tail_open_box() {
    let got = error_tail(
        "some preamble\n╷\n│ Error: something bad happened\n│ detail line\n",
        25,
    );
    assert_contains(&got, &["╷", "Error: something bad happened"]);
    assert!(!got.contains("some preamble"), "{got}");
}

#[test]
fn error_tail_box_block_long() {
    let got = error_tail(
        "╷\n│ line 1\n│ line 2\n│ line 3\n│ line 4\n│ line 5\n╵\n",
        3,
    );
    assert_contains(&got, &["│ line 1", "│ line 5", "╷", "╵"]);
}

#[test]
fn error_tail_error_line_long() {
    let got = error_tail(
        "some preamble\nError: first line\nline 2\nline 3\nline 4\nline 5\nline 6\n",
        3,
    );
    assert_contains(&got, &["Error: first line", "line 6"]);
    assert!(!got.contains("some preamble"), "{got}");
    assert_eq!(got.split('\n').count(), 6, "{got}");
}

#[test]
fn error_tail_strips_ansi() {
    let out = error_tail(
        "module.x: Creating...\n\x1b[31m╷\n│ \x1b[1mError:\x1b[0m\x1b[31m googleapi: Error 403: denied\n╵\x1b[0m\n",
        25,
    );
    assert!(!out.contains("\x1b["), "leaked ANSI: {out:?}");
    assert!(out.contains("Error 403"), "dropped the error: {out:?}");
}

// --- status_test.go ---

fn snap(f: impl FnOnce(&mut Snapshot)) -> Snapshot {
    let mut s = Snapshot::default();
    f(&mut s);
    s
}

fn lock(state: &str) -> ApplyLockVerdict {
    ApplyLockVerdict {
        state: state.into(),
        ..ApplyLockVerdict::default()
    }
}

#[test]
fn conclusion_cases() {
    let cases = [
        (
            "still planning",
            snap(|s| {
                s.total_stacks = 3;
                s.planned_stacks = 1;
            }),
            "",
        ),
        (
            "failed",
            snap(|s| {
                s.any_failed = true;
                s.finalized = true;
            }),
            "failure",
        ),
        ("clean finalized", snap(|s| s.finalized = true), "success"),
        (
            "gated awaiting stays in progress",
            snap(|s| {
                s.finalized = true;
                s.total_gates = 2;
                s.active_gates = 1;
            }),
            "",
        ),
        (
            "gated denied is action_required",
            snap(|s| {
                s.finalized = true;
                s.total_gates = 2;
                s.active_gates = 1;
                s.denied_gates = 1;
            }),
            "action_required",
        ),
        (
            "gated all active",
            snap(|s| {
                s.finalized = true;
                s.total_gates = 2;
                s.active_gates = 2;
            }),
            "success",
        ),
        (
            "failure beats gates",
            snap(|s| {
                s.any_failed = true;
                s.total_gates = 2;
            }),
            "failure",
        ),
    ];
    for (name, s, want) in cases {
        assert_eq!(conclusion(&s, &ApplyLockVerdict::default()), want, "{name}");
    }
}

#[test]
fn conclusion_lock_precedence() {
    let base = snap(|s| s.finalized = true);
    let held = ApplyLockVerdict {
        state: "held".into(),
        blocking_prs: vec![3],
        ..ApplyLockVerdict::default()
    };
    let cases = [
        (
            "clean plan, lock clear",
            base.clone(),
            lock("clear"),
            "success",
        ),
        (
            "clean plan, zero-value lock (not evaluated)",
            base.clone(),
            ApplyLockVerdict::default(),
            "success",
        ),
        (
            "clean plan, lock held stays in progress",
            base.clone(),
            held.clone(),
            "",
        ),
        (
            "lock unverifiable stays in progress",
            base.clone(),
            ApplyLockVerdict {
                reason: "x".into(),
                ..lock("unverifiable")
            },
            "",
        ),
        (
            "failure beats lock",
            snap(|s| {
                s.finalized = true;
                s.any_failed = true;
            }),
            held.clone(),
            "failure",
        ),
        (
            "awaiting gate stays in progress over lock",
            snap(|s| {
                s.finalized = true;
                s.total_gates = 1;
            }),
            held.clone(),
            "",
        ),
        (
            "denied gate beats lock",
            snap(|s| {
                s.finalized = true;
                s.total_gates = 1;
                s.denied_gates = 1;
            }),
            held.clone(),
            "action_required",
        ),
        (
            "satisfied gates then lock held",
            snap(|s| {
                s.finalized = true;
                s.total_gates = 1;
                s.active_gates = 1;
            }),
            held.clone(),
            "",
        ),
        (
            "satisfied gates, lock clear",
            snap(|s| {
                s.finalized = true;
                s.total_gates = 1;
                s.active_gates = 1;
            }),
            lock("clear"),
            "success",
        ),
        ("not finalized", Snapshot::default(), held, ""),
    ];
    for (name, s, l, want) in cases {
        assert_eq!(conclusion(&s, &l), want, "{name}");
    }
}

// --- triage_test.go ---

/// (name, detail, categories, class, cause substring, retryable).
type TriageCase<'a> = (&'a str, &'a str, &'a [Category], &'a str, &'a str, bool);

#[test]
fn classify_failure_cases() {
    let iam = vec![Category {
        name: "iam".into(),
        ..Category::default()
    }];
    let cases: &[TriageCase<'_>] = &[
        (
            "iam denied",
            "Error: Error 403: Permission 'run.services.setIamPolicy' denied",
            &iam,
            "iam_denied",
            "permission",
            true,
        ),
        (
            "state lock",
            "Error: Error acquiring the state lock\nLock Info:\n  ID: 1234",
            &[],
            "state_lock",
            "lock",
            true,
        ),
        (
            "quota",
            "Error 403: Quota exceeded for quota metric 'CPUs'",
            &[],
            "quota",
            "quota",
            false,
        ),
        (
            "already exists",
            "Error 409: ... alreadyExists",
            &[],
            "already_exists",
            "already exists",
            false,
        ),
        (
            "move failed",
            "cross-state move failed: dest push failed; source rolled back from .tfsp-state-backups",
            &[],
            "state_move",
            "state move",
            true,
        ),
        (
            "unknown raw",
            "panic: something totally novel",
            &[],
            "error",
            "",
            true,
        ),
        (
            "generic conflict not already_exists",
            "Error: provider configuration conflict between modules",
            &[],
            "error",
            "",
            true,
        ),
        (
            "generic does-not-have not iam",
            "Error: version constraint does not have a match",
            &[],
            "error",
            "",
            true,
        ),
    ];
    for (name, detail, cats, want_class, cause_has, retry) in cases {
        let tr = classify_failure(detail, cats);
        assert_eq!(tr.class, *want_class, "{name}: class");
        if cause_has.is_empty() {
            assert_eq!(tr.cause, "", "{name}: unknown error must have empty cause");
        } else {
            assert!(
                tr.cause.to_lowercase().contains(cause_has),
                "{name}: cause {:?} want substr {cause_has:?}",
                tr.cause
            );
        }
        assert_eq!(tr.retryable, *retry, "{name}: retryable");
        assert!(!tr.steps.is_empty(), "{name}: expected next steps");
    }
}

#[test]
fn classify_failure_state_impact() {
    let tr = classify_failure("cross-state move failed: ... .tfsp-state-backups/...", &[]);
    assert!(
        tr.state_impact.to_lowercase().contains("restore")
            || tr.state_impact.contains(".tfsp-state-backups"),
        "move failure should surface state impact, got {:?}",
        tr.state_impact
    );
}

// --- progress_weighted_test.go ---

fn plan_set() -> Vec<PhaseWeight> {
    [
        (Phase::WARMING, 1.0),
        (Phase::LINTING, 1.0),
        (Phase::INITIALIZING, 2.0),
        (Phase::PLANNING, 6.0),
    ]
    .into_iter()
    .map(|(p, weight)| PhaseWeight {
        phase: phase(p),
        weight,
    })
    .collect()
}

fn approx_pct(
    phases: &[PhaseWeight],
    p: &str,
    planned: usize,
    initialized: usize,
    total: usize,
    want: i64,
) {
    let (_, _, pct) = progress(phases, &phase(p), planned, initialized, total);
    assert_eq!(
        pct, want,
        "phase {p} (planned={planned} init={initialized} total={total})"
    );
}

#[test]
fn weighted_progress_bands() {
    let ps = plan_set();
    approx_pct(&ps, Phase::WARMING, 0, 0, 0, 10);
    approx_pct(&ps, Phase::LINTING, 0, 0, 0, 20);
    approx_pct(&ps, Phase::INITIALIZING, 0, 5, 10, 30);
    approx_pct(&ps, Phase::INITIALIZING, 0, 10, 10, 40);
    approx_pct(&ps, Phase::PLANNING, 5, 10, 10, 70);
    approx_pct(&ps, Phase::PLANNING, 10, 10, 10, 100);
}

#[test]
fn weighted_progress_fallback() {
    let (_, _, pct) = progress(&[], &phase(Phase::WARMING), 0, 0, 0);
    assert_eq!(pct, 5, "legacy warming pct");
    let (_, _, pct) = progress(&plan_set(), &phase(Phase::VERIFYING), 0, 0, 0);
    assert_eq!(pct, 100, "out-of-set verifying pct");
}

#[test]
fn phase_labels() {
    assert_eq!(
        phase_label(&phase(Phase::LINTING), 0, 0, 0),
        "linting modules…"
    );
    assert_eq!(phase_label(&phase(Phase::TESTING), 0, 0, 0), "testing…");
}
