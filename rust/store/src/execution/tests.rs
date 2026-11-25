// Ported from internal/store/execution_test.go (the projection writers and the
// reads they feed) and concurrency_test.go.
use std::thread;

use tfstackplan_gate::events::{Category, Counts, Edge, Status};

use super::*;
use crate::db::tests::temp_store;

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

#[test]
fn load_graph_after_projection() {
    let (_dir, s) = temp_store();
    let (e, stacks, edges) = sample_exec();
    seed_exec(&s, &e, &stacks, &edges);
    let g = s.load_graph("exec-1").unwrap();
    assert_eq!((g.stacks.len(), g.edges.len()), (2, 1));
    assert_eq!(g.stacks[0].path, "stacks/a");
    assert_eq!(g.stacks[0].status, Status::Pending);
    assert_eq!(g.stacks[1].status, Status::Running);
    assert_eq!(
        g.edges[0],
        Edge {
            from: "stacks/a".into(),
            to: "stacks/b".into()
        }
    );
}

#[test]
fn get_execution() {
    let (_dir, s) = temp_store();
    let (e, stacks, edges) = sample_exec();
    seed_exec(&s, &e, &stacks, &edges);
    let got = s.get_execution("exec-1").unwrap().unwrap();
    assert_eq!(
        (got.repo.as_str(), got.pr, got.environment.as_str()),
        ("owner/repo", 42, "staging")
    );
    assert_eq!(got.status_context, "iam/staging");
    assert!(s.get_execution("nope").unwrap().is_none());
}

#[test]
fn load_graph_surfaces_counts() {
    let (_dir, s) = temp_store();
    seed_exec(
        &s,
        &ProjectedExecution {
            id: "e1".into(),
            ..ProjectedExecution::default()
        },
        &[ProjectedStack {
            path: "a".into(),
            ..ProjectedStack::default()
        }],
        &[],
    );
    s.conn()
        .execute(
            "UPDATE stacks SET counts = ?1 WHERE execution_id = 'e1' AND stack_path = 'a'",
            [r#"{"add":6,"change":2}"#],
        )
        .unwrap();
    let g = s.load_graph("e1").unwrap();
    let c = g.stacks[0].counts.unwrap();
    assert_eq!((c.add, c.change), (6, 2));
}

#[test]
fn latest_execution_id() {
    let (_dir, s) = temp_store();
    assert_eq!(s.latest_execution_id(7, "prod").unwrap(), None);
    let (mut e, stacks, edges) = sample_exec();
    e.id = "exec-latest-1".into();
    e.pr = 7;
    e.environment = "prod".into();
    seed_exec(&s, &e, &stacks, &edges);
    assert_eq!(
        s.latest_execution_id(7, "prod").unwrap().as_deref(),
        Some("exec-latest-1")
    );
}

#[test]
fn phases_for_orders_oldest_first() {
    let (_dir, s) = temp_store();
    let (e, stacks, edges) = sample_exec();
    seed_exec(&s, &e, &stacks, &edges);
    s.write(|tx| {
        for ph in ["warming", "planning", "report"] {
            append_phase_history(tx, "exec-1", ph, "", None)?;
        }
        Ok(())
    })
    .unwrap();
    let rows = s.phases_for("exec-1").unwrap();
    assert_eq!(
        rows.iter().map(|r| r.phase.as_str()).collect::<Vec<_>>(),
        ["warming", "planning", "report"]
    );
}

#[test]
fn phases_for_empty() {
    let (_dir, s) = temp_store();
    assert!(s.phases_for("nope").unwrap().is_empty());
}

#[test]
fn project_execution_row_owned_columns_only() {
    let (_dir, s) = temp_store();
    s.write(|tx| {
        tx.execute(
            "INSERT INTO executions (id, repo, sha, status, report_markdown) VALUES ('e1','seed-repo','seed-sha','in_progress','REPORT')",
            [],
        )?;
        project_execution_row(
            tx,
            &ProjectedExecution {
                id: "e1".into(),
                repo: "r".into(),
                sha: "abc".into(),
                pr: 7,
                environment: "nonprod".into(),
                context: "terraform/nonprod".into(),
                phase: "applying".into(),
                status: "success".into(),
                ..ProjectedExecution::default()
            },
        )
    })
    .unwrap();
    let e = s.get_execution("e1").unwrap().unwrap();
    assert_eq!(
        (e.repo.as_str(), e.pr, e.status.as_str(), e.phase.as_str()),
        ("r", 7, "success", "applying")
    );
    assert_eq!(e.report_markdown, "REPORT");
}

#[test]
fn project_execution_row_owns_superseded_by() {
    let (_dir, s) = temp_store();
    s.write(|tx| {
        project_execution_row(
            tx,
            &ProjectedExecution {
                id: "e1".into(),
                repo: "r".into(),
                sha: "s".into(),
                status: "in_progress".into(),
                ..ProjectedExecution::default()
            },
        )?;
        project_execution_row(
            tx,
            &ProjectedExecution {
                id: "e1".into(),
                status: "in_progress".into(),
                superseded_by: "e2".into(),
                ..ProjectedExecution::default()
            },
        )
    })
    .unwrap();
    assert_eq!(s.get_execution("e1").unwrap().unwrap().superseded_by, "e2");
}

#[test]
fn project_execution_row_revives_created_at_on_re_init() {
    let (_dir, s) = temp_store();
    s.conn()
        .execute(
            "INSERT INTO executions (id, repo, sha, status, superseded_by, created_at)
             VALUES ('e1','r','s','success','e2','2000-01-01T00:00:00Z')",
            [],
        )
        .unwrap();
    s.write(|tx| {
        project_execution_row(
            tx,
            &ProjectedExecution {
                id: "e1".into(),
                repo: "r".into(),
                sha: "s".into(),
                status: "in_progress".into(),
                ..ProjectedExecution::default()
            },
        )
    })
    .unwrap();
    let e = s.get_execution("e1").unwrap().unwrap();
    assert_eq!(e.superseded_by, "");
    assert_ne!(e.created_at.strftime("%Y").to_string(), "2000");
}

#[test]
fn project_execution_row_tick_after_supersede_does_not_reset_created_at() {
    let (_dir, s) = temp_store();
    s.conn()
        .execute(
            "INSERT INTO executions (id, repo, sha, status, superseded_by, created_at)
             VALUES ('e1','r','s','in_progress','', '2000-01-01T00:00:00Z')",
            [],
        )
        .unwrap();
    let superseded = ProjectedExecution {
        id: "e1".into(),
        status: "in_progress".into(),
        superseded_by: "e2".into(),
        ..ProjectedExecution::default()
    };
    let year = |s: &Store| {
        s.get_execution("e1")
            .unwrap()
            .unwrap()
            .created_at
            .strftime("%Y")
            .to_string()
    };
    // Superseding a live execution does not reset created_at,
    s.write(|tx| project_execution_row(tx, &superseded))
        .unwrap();
    assert_eq!(year(&s), "2000");
    // and neither does a late tick that re-projects the same folded state.
    s.write(|tx| project_execution_row(tx, &superseded))
        .unwrap();
    assert_eq!(s.get_execution("e1").unwrap().unwrap().superseded_by, "e2");
    assert_eq!(year(&s), "2000");
}

#[test]
fn project_stack_edge_and_phase_history_round_trip() {
    let (_dir, s) = temp_store();
    s.write(|tx| {
        tx.execute(
            "INSERT INTO executions (id, repo, sha) VALUES ('e1','r','sha1')",
            [],
        )?;
        project_stack(
            tx,
            "e1",
            &ProjectedStack {
                path: "stacks/a".into(),
                project: "proj-a".into(),
                status: Status::Running,
                detail: "running now".into(),
                categories: vec![Category {
                    name: "iam".into(),
                    ..Category::default()
                }],
                counts: Some(Counts {
                    add: 3,
                    change: 1,
                    ..Counts::default()
                }),
            },
        )?;
        project_stack(
            tx,
            "e1",
            &ProjectedStack {
                path: "stacks/b".into(),
                project: "proj-b".into(),
                status: Status::Pending,
                ..ProjectedStack::default()
            },
        )?;
        project_edge(tx, "e1", "stacks/a", "stacks/b")?;
        // A duplicate edge is ignored.
        project_edge(tx, "e1", "stacks/a", "stacks/b")?;
        append_phase_history(tx, "e1", "applying", "applying stacks...", Some(50))
    })
    .unwrap();
    let g = s.load_graph("e1").unwrap();
    assert_eq!((g.stacks.len(), g.edges.len()), (2, 1));
    assert_eq!(g.stacks[0].status, Status::Running);
    assert_eq!(g.stacks[0].detail, "running now");
    assert_eq!(g.stacks[0].counts.map(|c| c.add), Some(3));
    assert_eq!(g.stacks[0].categories[0].name, "iam");
    let rows = s.phases_for("e1").unwrap();
    assert_eq!(rows.len(), 1);
    assert_eq!(
        (
            rows[0].phase.as_str(),
            rows[0].label.as_str(),
            rows[0].progress_pct
        ),
        ("applying", "applying stacks...", Some(50))
    );
}

/// concurrency_test.go: many threads ticking one stack through their own
/// connections; every write lands and the graph still loads.
#[test]
fn store_concurrency() {
    let (dir, s) = temp_store();
    let path = dir.path().join("test.db");
    seed_exec(
        &s,
        &ProjectedExecution {
            id: "concurrent-exec-1".into(),
            repo: "owner/repo".into(),
            sha: "abc123abc123".into(),
            pr: 100,
            environment: "production".into(),
            status: "in_progress".into(),
            ..ProjectedExecution::default()
        },
        &[ProjectedStack {
            path: "stacks/a".into(),
            status: Status::Pending,
            ..ProjectedStack::default()
        }],
        &[],
    );
    const WORKERS: usize = 20;
    let stores: Vec<Store> = (0..WORKERS).map(|_| Store::open(&path).unwrap()).collect();
    let errors: Vec<String> = thread::scope(|scope| {
        let handles: Vec<_> = stores
            .iter()
            .enumerate()
            .map(|(w, store)| {
                scope.spawn(move || {
                    for j in 0..5 {
                        let status = if j % 2 == 0 {
                            Status::Safe
                        } else {
                            Status::Running
                        };
                        let st = ProjectedStack {
                            path: "stacks/a".into(),
                            status,
                            detail: format!("Log from worker {w} step {j}"),
                            ..ProjectedStack::default()
                        };
                        store
                            .write(|tx| project_stack(tx, "concurrent-exec-1", &st))
                            .map_err(|e| format!("worker {w} step {j}: {e}"))?;
                    }
                    Ok::<(), String>(())
                })
            })
            .collect();
        handles
            .into_iter()
            .filter_map(|h| h.join().unwrap().err())
            .collect()
    });
    assert!(errors.is_empty(), "{errors:?}");
    let g = s.load_graph("concurrent-exec-1").unwrap();
    assert_eq!(g.stacks.len(), 1);
    assert_eq!(g.stacks[0].path, "stacks/a");
}
