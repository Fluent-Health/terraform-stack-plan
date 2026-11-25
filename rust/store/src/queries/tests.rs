// Ported from internal/store/prmeta_test.go, output_test.go, applylock_checks_test.go
// and the list/lookup cases of execution_test.go.
use tfstackplan_gate::events::Status;

use super::*;
use crate::db::tests::temp_store;
use crate::execution::{
    ProjectedExecution, ProjectedStack, project_edge, project_execution_row, project_stack,
};

fn sample_exec() -> (
    ProjectedExecution,
    Vec<ProjectedStack>,
    Vec<(&'static str, &'static str)>,
) {
    let e = ProjectedExecution {
        id: "exec-1".into(),
        repo: "owner/repo".into(),
        sha: "abc123".into(),
        pr: 42,
        environment: "staging".into(),
        log_url: "https://ci/log".into(),
        context: "iam/staging".into(),
        status: "in_progress".into(),
        ..ProjectedExecution::default()
    };
    let stacks = vec![
        ProjectedStack {
            path: "stacks/a".into(),
            project: "proj-a".into(),
            status: Status::Pending,
            ..ProjectedStack::default()
        },
        ProjectedStack {
            path: "stacks/b".into(),
            project: "proj-b".into(),
            status: Status::Running,
            ..ProjectedStack::default()
        },
    ];
    (e, stacks, vec![("stacks/a", "stacks/b")])
}

fn seed_exec(s: &Store, e: &ProjectedExecution, stacks: &[ProjectedStack], edges: &[(&str, &str)]) {
    s.write(|tx| {
        project_execution_row(tx, e)?;
        for st in stacks {
            project_stack(tx, &e.id, st)?;
        }
        for (from, to) in edges {
            project_edge(tx, &e.id, from, to)?;
        }
        Ok(())
    })
    .unwrap();
}

fn exec(id: &str, pr: i64, env: &str) -> ProjectedExecution {
    ProjectedExecution {
        id: id.into(),
        repo: "o/r".into(),
        pr,
        environment: env.into(),
        ..ProjectedExecution::default()
    }
}

#[test]
fn get_pr_meta_absent() {
    let (_dir, s) = temp_store();
    assert_eq!(s.get_pr_meta("owner/repo", 42).unwrap(), None);
}

fn widget(pr: i64) -> PrMeta {
    PrMeta {
        repo: "owner/repo".into(),
        pr,
        title: "Add widget".into(),
        body: "Some body".into(),
        author_login: "octocat".into(),
        head_ref: "feature/widget".into(),
        url: format!("https://github.com/owner/repo/pull/{pr}"),
        auto_merge: true,
        updated_at: None,
    }
}

#[test]
fn upsert_pr_meta_and_get() {
    let (_dir, s) = temp_store();
    let m = widget(42);
    s.upsert_pr_meta(&m).unwrap();
    let got = s.get_pr_meta("owner/repo", 42).unwrap().expect("row");
    assert!(got.updated_at.is_some(), "UpdatedAt is zero");
    assert_eq!(
        PrMeta {
            updated_at: None,
            ..got
        },
        m
    );
}

#[test]
fn get_pr_meta_by_pr_found_without_repo() {
    let (_dir, s) = temp_store();
    let m = widget(999);
    s.upsert_pr_meta(&m).unwrap();
    let got = s.get_pr_meta_by_pr(999).unwrap().expect("row");
    assert_eq!(got.repo, "owner/repo");
    assert_eq!(
        PrMeta {
            updated_at: None,
            ..got
        },
        m
    );
}

#[test]
fn get_pr_meta_by_pr_absent() {
    let (_dir, s) = temp_store();
    assert_eq!(s.get_pr_meta_by_pr(42).unwrap(), None);
}

#[test]
fn upsert_pr_meta_updates_in_place() {
    let (_dir, s) = temp_store();
    let mut m = PrMeta {
        repo: "owner/repo".into(),
        pr: 42,
        title: "Original title".into(),
        author_login: "octocat".into(),
        ..PrMeta::default()
    };
    s.upsert_pr_meta(&m).unwrap();
    m.title = "Changed title".into();
    s.upsert_pr_meta(&m).unwrap();
    let got = s.get_pr_meta("owner/repo", 42).unwrap().expect("row");
    assert_eq!(got.title, "Changed title");
    let count: i64 = s
        .read(|c| {
            c.query_row(
                "SELECT count(*) FROM pr_meta WHERE repo = 'owner/repo' AND pr = 42",
                [],
                |r| r.get(0),
            )
        })
        .unwrap();
    assert_eq!(count, 1, "upsert must not duplicate");
}

#[test]
fn stack_output_upsert_and_get() {
    let (_dir, s) = temp_store();
    s.upsert_stack_output("e1", "stacks/a", "log", "", "tail-excerpt")
        .unwrap();
    s.upsert_stack_output("e1", "stacks/a", "log", "obj/key", "more-excerpt")
        .unwrap();
    assert_eq!(
        s.get_stack_output("e1", "stacks/a", "log").unwrap(),
        Some(("obj/key".into(), "more-excerpt".into()))
    );
    assert_eq!(
        s.get_stack_output("e1", "stacks/a", "verify").unwrap(),
        None,
        "unset kind"
    );
}

#[test]
fn apply_lock_check_round_trip() {
    let (_dir, s) = temp_store();
    let c = ApplyLockCheck {
        environment: "prod".into(),
        head_sha: "sha1".into(),
        check_run_id: 99,
        pr: 7,
        stacks: vec!["a".into(), "b".into()],
        state: "held".into(),
        kind: "merge_group".into(),
        ..ApplyLockCheck::default()
    };
    s.upsert_apply_lock_check(&c).unwrap();
    let got = s
        .get_apply_lock_check("prod", "sha1")
        .unwrap()
        .expect("row");
    assert_eq!(got, c);
    let c2 = ApplyLockCheck {
        head_sha: "sha2".into(),
        state: "clear".into(),
        ..c
    };
    s.upsert_apply_lock_check(&c2).unwrap();
    let held = s.held_apply_lock_checks("prod").unwrap();
    assert_eq!(held.len(), 1);
    assert_eq!(held[0].head_sha, "sha1");
}

#[test]
fn apply_lock_check_execution_id_round_trip() {
    let (_dir, s) = temp_store();
    let rec = |state: &str, id: &str| ApplyLockCheck {
        environment: "nonprod".into(),
        head_sha: "sha1".into(),
        check_run_id: 42,
        pr: 7,
        repo: "o/r".into(),
        stacks: vec!["stacks/a".into()],
        state: state.into(),
        kind: "pr_head".into(),
        execution_id: id.into(),
    };
    s.upsert_apply_lock_check(&rec("held", "run-7-nonprod-plan-abc-a1"))
        .unwrap();
    let c = s
        .get_apply_lock_check("nonprod", "sha1")
        .unwrap()
        .expect("row");
    assert_eq!(c.execution_id, "run-7-nonprod-plan-abc-a1");
    let held = s.held_apply_lock_checks("nonprod").unwrap();
    assert_eq!(held.len(), 1);
    assert_eq!(held[0].execution_id, "run-7-nonprod-plan-abc-a1");
    s.upsert_apply_lock_check(&rec("clear", "run-7-nonprod-plan-def-b2"))
        .unwrap();
    let c = s
        .get_apply_lock_check("nonprod", "sha1")
        .unwrap()
        .expect("row");
    assert_eq!(c.execution_id, "run-7-nonprod-plan-def-b2");
    assert_eq!(c.state, "clear");
}

#[test]
fn list_executions() {
    let (_dir, s) = temp_store();
    for e in [
        exec("e1", 1, "staging"),
        exec("e2", 2, "prod"),
        exec("e3", 1, "staging"),
    ] {
        seed_exec(&s, &e, &[], &[]);
    }
    assert_eq!(s.list_executions(10).unwrap().len(), 3);
    assert_eq!(s.list_executions(2).unwrap().len(), 2);
    let pr1 = s.list_executions_for_pr(1).unwrap();
    assert_eq!(pr1.len(), 2);
    for e in pr1 {
        assert_eq!(e.pr, 1, "PR filter leaked {}", e.id);
        assert_eq!(e.repo, "o/r");
    }
}

#[test]
fn latest_verify_execution_id() {
    let (_dir, s) = temp_store();
    for (id, ctx) in [
        ("plan-1", "plan/staging"),
        ("verify-1", "verify/staging"),
        ("verify-2", "verify/staging"),
    ] {
        seed_exec(
            &s,
            &ProjectedExecution {
                context: ctx.into(),
                ..exec(id, 7, "staging")
            },
            &[],
            &[],
        );
    }
    let id = s
        .latest_verify_execution_id(7, "staging")
        .expect("a verify execution");
    assert!(id == "verify-1" || id == "verify-2", "latest verify = {id}");
    assert_eq!(s.latest_verify_execution_id(99, "staging"), None);
}

#[test]
fn report_rev_and_check_run_id() {
    let (_dir, s) = temp_store();
    let (e, stacks, edges) = sample_exec();
    seed_exec(&s, &e, &stacks, &edges);
    s.write(|tx| {
        project_stack(
            tx,
            "exec-1",
            &ProjectedStack {
                path: "stacks/a".into(),
                status: Status::Failed,
                detail: "boom".into(),
                ..ProjectedStack::default()
            },
        )
    })
    .unwrap();
    let g = s.load_graph("exec-1").unwrap();
    assert_eq!(g.stacks[0].status, Status::Failed);
    assert_eq!(g.stacks[0].detail, "boom");
    s.set_report("exec-1", "# report").unwrap();
    s.bump_rev("exec-1").unwrap();
    s.set_check_run_id("exec-1", 12345).unwrap();
    let got = s.get_execution("exec-1").unwrap().expect("row");
    assert_eq!(got.report_markdown, "# report");
    assert_eq!(got.rev, 1);
    assert_eq!(got.check_run_id, Some(12345));
}

#[test]
fn find_and_supersede_execution() {
    let (_dir, s) = temp_store();
    let (mut exec1, stacks, edges) = sample_exec();
    seed_exec(&s, &exec1, &stacks, &edges);
    let find = |incoming: &str| {
        s.find_non_superseded_execution(
            exec1.pr,
            &exec1.environment,
            &exec1.sha,
            &exec1.context,
            incoming,
        )
        .unwrap()
    };
    assert_eq!(
        find("exec-1"),
        None,
        "found non-superseded execution prematurely"
    );
    let exec2 = ProjectedExecution {
        id: "exec-2".into(),
        ..exec1.clone()
    };
    seed_exec(&s, &exec2, &stacks, &edges);
    assert_eq!(find("exec-2"), Some("exec-1".into()));
    exec1.superseded_by = "exec-2".into();
    seed_exec(&s, &exec1, &[], &[]);
    assert_eq!(
        s.get_execution("exec-1")
            .unwrap()
            .expect("row")
            .superseded_by,
        "exec-2"
    );
    assert_eq!(find("exec-2"), None, "should not find superseded execution");
}

#[test]
fn environments_for_pr() {
    let (_dir, s) = temp_store();
    let (e, stacks, edges) = sample_exec();
    for (id, env) in [("exec-env-1", "prod"), ("exec-env-2", "staging")] {
        seed_exec(
            &s,
            &ProjectedExecution {
                id: id.into(),
                pr: 12,
                environment: env.into(),
                ..e.clone()
            },
            &stacks,
            &edges,
        );
    }
    let mut envs = s.environments_for_pr(12).unwrap();
    envs.sort();
    assert_eq!(envs, vec!["prod".to_string(), "staging".to_string()]);
}

#[test]
fn find_execution_by_sha() {
    let (_dir, s) = temp_store();
    let row = |id: &str, pr: i64| ProjectedExecution {
        sha: "sha1".into(),
        context: "plan/nonprod".into(),
        ..exec(id, pr, "nonprod")
    };
    seed_exec(&s, &row("e-serve", 9), &[], &[]);
    seed_exec(&s, &row("e-noprfoo", 0), &[], &[]);
    assert_eq!(
        s.find_execution_by_sha("nonprod", "plan/nonprod", "sha1")
            .unwrap(),
        Some("e-serve".into())
    );
    seed_exec(
        &s,
        &ProjectedExecution {
            id: "e-serve".into(),
            superseded_by: "e-newer".into(),
            ..ProjectedExecution::default()
        },
        &[],
        &[],
    );
    assert_eq!(
        s.find_execution_by_sha("nonprod", "plan/nonprod", "sha1")
            .unwrap(),
        None
    );
    assert_eq!(
        s.find_execution_by_sha("nonprod", "plan/nonprod", "absent")
            .unwrap(),
        None
    );
}

#[test]
fn latest_reported_execution_id_skips_queued_rows() {
    let (_dir, s) = temp_store();
    seed_exec(
        &s,
        &ProjectedExecution {
            phase: "planning".into(),
            ..exec("reported", 5, "nonprod")
        },
        &[],
        &[],
    );
    assert_eq!(
        s.latest_reported_execution_id(5, "nonprod"),
        Some("reported".into())
    );
    assert_eq!(s.latest_reported_execution_id(6, "nonprod"), None);
}

#[test]
fn latest_pr_for_sha() {
    let (_dir, s) = temp_store();
    seed_exec(
        &s,
        &ProjectedExecution {
            sha: "m1".into(),
            context: "apply/nonprod".into(),
            ..exec("a1", 4, "nonprod")
        },
        &[],
        &[],
    );
    assert_eq!(
        s.latest_pr_for_sha("m1", "nonprod", "apply/nonprod"),
        Some(4)
    );
    assert_eq!(s.latest_pr_for_sha("m1", "nonprod", "plan/nonprod"), None);
}

#[test]
fn stuck_pending_executions() {
    let (_dir, s) = temp_store();
    seed_exec(
        &s,
        &ProjectedExecution {
            status: "in_progress".into(),
            ..exec("run-1-nonprod-plan-abc-a1", 1, "nonprod")
        },
        &[],
        &[],
    );
    seed_exec(
        &s,
        &ProjectedExecution {
            status: "in_progress".into(),
            ..exec("runner-made", 1, "nonprod")
        },
        &[],
        &[],
    );
    let later = Timestamp::now() + jiff::SignedDuration::from_hours(1);
    let stuck = s.stuck_pending_executions("nonprod", later).unwrap();
    assert_eq!(stuck.len(), 1);
    assert_eq!(stuck[0].id, "run-1-nonprod-plan-abc-a1");
    let earlier = Timestamp::now() - jiff::SignedDuration::from_hours(1);
    assert!(
        s.stuck_pending_executions("nonprod", earlier)
            .unwrap()
            .is_empty()
    );
}
