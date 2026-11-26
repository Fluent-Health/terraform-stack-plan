// Ported from internal/server/lifecyclehandlers_test.go (plus two Rust-only checks).

#[test]
fn exec_context_kinds() {
    assert_eq!(super::exec_context_kind("plan/staging"), "plan");
    assert_eq!(super::exec_context_kind("apply"), "apply");
    assert_eq!(super::exec_context_kind("verify/x"), "verify");
    assert_eq!(super::exec_context_kind("iam/x"), "other");
}

#[tokio::test]
async fn phases_seeded_through_the_aggregate_render() {
    use tfstackplan_gate::events::{Init, Phase, PhaseEvent};
    let (_d, a) = crate::testutil::app();
    seed_init(
        &a,
        Init {
            id: "p1".into(),
            pr: 3,
            context: "plan".into(),
            ..Init::default()
        },
    )
    .await;
    seed_phase(
        &a,
        PhaseEvent {
            id: "p1".into(),
            phase: Phase::new("planning"),
            ..PhaseEvent::default()
        },
    )
    .await;
    let r = send(&a, "GET", "/api/lifecycle?pr=3", "", "").await;
    assert_eq!(r.status, 200);
    assert!(r.text().contains(r#""key":"plan""#), "{}", r.text());
}

// --- lifecyclehandlers_test.go ---

use std::collections::HashMap;
use std::sync::Arc;

use serde_json::Value;
use tfstackplan_gate::events::{Counts, Init, Phase, PhaseEvent, StackState, Status};

use crate::App;
use crate::testutil::{seed_init, seed_phase, seed_terminal_status, send};

fn staging_app() -> (tempfile::TempDir, Arc<App>) {
    crate::testutil::app_with(
        Arc::new(tfstackplan_server_clients::github::MockGitHub::default()),
        crate::Config {
            environment: "staging".into(),
            ..crate::Config::default()
        },
    )
}

fn stack(path: &str, status: Status, add: i64, change: i64) -> StackState {
    StackState {
        path: path.into(),
        status,
        counts: (add > 0 || change > 0).then_some(Counts {
            add,
            change,
            ..Counts::default()
        }),
        ..StackState::default()
    }
}

fn init(id: &str, sha: &str, pr: i64, context: &str, stacks: Vec<StackState>) -> Init {
    Init {
        id: id.into(),
        repo: "o/r".into(),
        sha: sha.into(),
        pr,
        environment: "staging".into(),
        context: context.into(),
        stacks,
        ..Init::default()
    }
}

async fn phases(app: &App, id: &str, pr: i64, context: &str, ps: &[&str]) {
    for p in ps {
        seed_phase(
            app,
            PhaseEvent {
                id: id.into(),
                phase: Phase::new(p),
                pr,
                environment: "staging".into(),
                context: context.into(),
                ..PhaseEvent::default()
            },
        )
        .await;
    }
}

/// Go's `getLifecycle`.
async fn get_lifecycle(app: &Arc<App>, pr: i64) -> Vec<Value> {
    let w = send(app, "GET", &format!("/api/lifecycle?pr={pr}"), "", "").await;
    assert_eq!(w.status, 200, "{}", w.text());
    serde_json::from_slice(&w.body).expect("lifecycle json")
}

fn keys(out: &[Value]) -> Vec<String> {
    out.iter()
        .map(|p| p["key"].as_str().unwrap_or_default().to_string())
        .collect()
}

fn by_key(out: &[Value]) -> HashMap<String, Value> {
    out.iter()
        .map(|p| (p["key"].as_str().unwrap_or_default().to_string(), p.clone()))
        .collect()
}

fn set_created(app: &App, id: &str, ts: &str) {
    let (id, ts) = (id.to_string(), ts.to_string());
    app.store()
        .exec(|c| {
            c.execute(
                "UPDATE executions SET created_at = ?1 WHERE id = ?2",
                rusqlite::params![ts, id],
            )
        })
        .expect("set created_at");
}

#[tokio::test]
async fn get_lifecycle_fold() {
    let (_d, a) = staging_app();
    seed_init(
        &a,
        init(
            "plan-1",
            "sha",
            42,
            "plan/staging",
            vec![
                stack("projects/a", Status::Planned, 2, 0),
                stack("projects/b", Status::Planned, 0, 1),
            ],
        ),
    )
    .await;
    phases(
        &a,
        "plan-1",
        42,
        "plan/staging",
        &[Phase::WARMING, Phase::PLANNING, Phase::REPORT],
    )
    .await;
    a.store()
        .exec(|c| {
            c.execute(
                "INSERT INTO gate_targets (pr, environment, class, target, grant_name, state, requester)
                 VALUES (42, 'staging', 'iam', 'projects/a', '', 'AWAITING', '')",
                [],
            )
        })
        .expect("seed target");
    let out = get_lifecycle(&a, 42).await;
    let k = by_key(&out);
    assert_eq!(k["prepare"]["state"], "done", "{out:?}");
    assert_eq!(k["plan"]["state"], "done", "{out:?}");
    assert_eq!(k["report"]["state"], "now", "{out:?}");
    assert_eq!(k["report"]["result"], "+2 ~1", "{out:?}");
    assert_eq!(k["classify"]["result"], "1 gate required", "{out:?}");
    assert_eq!(k["approve"]["state"], "pending", "{out:?}");
    assert_eq!(k["approve"]["result"], "waits on approval", "{out:?}");
    for key in ["apply", "verify", "moves", "init"] {
        assert!(
            !k.contains_key(key),
            "{key} on a plan-only PR: {:?}",
            keys(&out)
        );
    }
}

#[tokio::test]
async fn get_lifecycle_terminal_success_is_done() {
    let (_d, a) = staging_app();
    seed_init(
        &a,
        init(
            "plan-1",
            "sha",
            42,
            "plan/staging",
            vec![stack("projects/a", Status::Planned, 2, 0)],
        ),
    )
    .await;
    phases(
        &a,
        "plan-1",
        42,
        "plan/staging",
        &[Phase::WARMING, Phase::PLANNING, Phase::REPORT],
    )
    .await;
    seed_terminal_status(&a, "plan-1", "success").await;
    let out = get_lifecycle(&a, 42).await;
    assert_eq!(by_key(&out)["report"]["state"], "done", "{out:?}");
}

#[tokio::test]
async fn get_lifecycle_uses_newest_execution_per_context() {
    let (_d, a) = staging_app();
    for (i, id) in ["plan-a1", "plan-a2", "plan-a3"].into_iter().enumerate() {
        seed_init(
            &a,
            init(
                id,
                &format!("sha{i}"),
                42,
                "plan/staging",
                vec![stack("projects/a", Status::Planned, 1, 0)],
            ),
        )
        .await;
        set_created(&a, id, &format!("2026-07-15 04:{:02}:00", 10 + i * 10));
        phases(
            &a,
            id,
            42,
            "plan/staging",
            &[
                Phase::WARMING,
                Phase::LINTING,
                Phase::PLANNING,
                Phase::REPORT,
            ],
        )
        .await;
    }
    let out = get_lifecycle(&a, 42).await;
    let mut seen: HashMap<String, usize> = HashMap::new();
    for k in keys(&out) {
        *seen.entry(k).or_default() += 1;
    }
    assert!(seen.values().all(|n| *n == 1), "{:?}", keys(&out));
    for k in ["prepare", "linting", "plan", "report"] {
        assert_eq!(seen.get(k), Some(&1), "{:?}", keys(&out));
    }
}

#[tokio::test]
async fn get_lifecycle_coalesces_repeated_phase_keys() {
    let (_d, a) = staging_app();
    seed_init(
        &a,
        init(
            "plan-1",
            "sha",
            42,
            "plan/staging",
            vec![stack("projects/a", Status::Planned, 0, 0)],
        ),
    )
    .await;
    phases(
        &a,
        "plan-1",
        42,
        "plan/staging",
        &[
            Phase::WARMING,
            Phase::LINTING,
            Phase::WARMING,
            Phase::PLANNING,
        ],
    )
    .await;
    let out = get_lifecycle(&a, 42).await;
    let ks = keys(&out);
    assert_eq!(ks.iter().filter(|k| *k == "prepare").count(), 1, "{ks:?}");
    assert_eq!(by_key(&out)["plan"]["state"], "now", "{out:?}");
    assert_eq!(ks.first().map(String::as_str), Some("prepare"), "{ks:?}");
}

#[tokio::test]
async fn get_lifecycle_plan_only_hides_apply_side() {
    let (_d, a) = staging_app();
    seed_init(
        &a,
        init(
            "plan-1",
            "sha",
            42,
            "plan/staging",
            vec![
                stack("projects/a", Status::Planned, 1, 0),
                stack("projects/b", Status::Pending, 0, 0),
            ],
        ),
    )
    .await;
    phases(
        &a,
        "plan-1",
        42,
        "plan/staging",
        &[Phase::WARMING, Phase::LINTING, Phase::PLANNING],
    )
    .await;
    let out = get_lifecycle(&a, 42).await;
    let k = by_key(&out);
    for key in ["init", "moves", "apply", "verify", "approve"] {
        assert!(!k.contains_key(key), "{key}: {:?}", keys(&out));
    }
    for key in ["classify", "report"] {
        assert_eq!(k[key]["state"], "pending", "{key}: {out:?}");
    }
    assert_eq!(k["plan"]["state"], "now", "{out:?}");
    assert_eq!(k["plan"]["progress_pct"], 50, "{out:?}");
}

#[tokio::test]
async fn get_lifecycle_apply_phases_stay_on_apply_side() {
    let (_d, a) = staging_app();
    seed_init(
        &a,
        init(
            "plan-1",
            "sha",
            42,
            "plan/staging",
            vec![stack("projects/a", Status::Planned, 1, 0)],
        ),
    )
    .await;
    phases(
        &a,
        "plan-1",
        42,
        "plan/staging",
        &[
            Phase::WARMING,
            Phase::PLANNING,
            Phase::CLASSIFY,
            Phase::REPORT,
        ],
    )
    .await;
    seed_terminal_status(&a, "plan-1", "success").await;
    seed_init(
        &a,
        init(
            "apply-1",
            "sha",
            42,
            "apply/staging",
            vec![
                stack("projects/a", Status::Safe, 0, 0),
                stack("projects/b", Status::Pending, 0, 0),
            ],
        ),
    )
    .await;
    phases(&a, "apply-1", 42, "apply/staging", &[Phase::WARMING]).await;
    let out = get_lifecycle(&a, 42).await;
    let k = by_key(&out);
    for key in ["prepare", "plan", "classify", "report"] {
        assert_eq!(k[key]["state"], "done", "{key}: {out:?}");
    }
    assert_eq!(k["apply"]["state"], "now", "{out:?}");
    assert_eq!(k["apply"]["detail"], "warming caches", "{out:?}");
    assert_eq!(k["apply"]["progress_pct"], 50, "{out:?}");
    assert_eq!(k["verify"]["state"], "pending", "{out:?}");
    assert!(!k.contains_key("moves"), "{:?}", keys(&out));
    let ks = keys(&out);
    let idx = |key: &str| ks.iter().position(|k| k == key).expect(key);
    assert!(
        idx("report") < idx("apply") && idx("apply") < idx("verify"),
        "order wrong: {ks:?}"
    );
}

#[tokio::test]
async fn get_lifecycle_moves_only_when_moving_stacks_known() {
    let (_d, a) = staging_app();
    seed_init(
        &a,
        init(
            "plan-1",
            "sha",
            42,
            "plan/staging",
            vec![stack("projects/a", Status::Moving, 0, 0)],
        ),
    )
    .await;
    phases(&a, "plan-1", 42, "plan/staging", &[Phase::REPORT]).await;
    seed_terminal_status(&a, "plan-1", "success").await;
    seed_init(
        &a,
        init(
            "apply-1",
            "sha",
            42,
            "apply/staging",
            vec![stack("projects/a", Status::Pending, 0, 0)],
        ),
    )
    .await;
    phases(&a, "apply-1", 42, "apply/staging", &[Phase::WARMING]).await;
    let out = get_lifecycle(&a, 42).await;
    assert!(
        out.iter()
            .any(|p| p["key"] == "moves" && p["state"] == "pending"),
        "{:?}",
        keys(&out)
    );
}

#[tokio::test]
async fn get_lifecycle_verify_done_when_applied() {
    let (_d, a) = staging_app();
    seed_init(
        &a,
        init(
            "plan-1",
            "sha",
            42,
            "plan/staging",
            vec![stack("projects/a", Status::Planned, 1, 0)],
        ),
    )
    .await;
    phases(
        &a,
        "plan-1",
        42,
        "plan/staging",
        &[Phase::WARMING, Phase::PLANNING, Phase::REPORT],
    )
    .await;
    seed_terminal_status(&a, "plan-1", "success").await;
    seed_init(
        &a,
        init(
            "apply-1",
            "sha",
            42,
            "apply/staging",
            vec![stack("projects/a", Status::Safe, 0, 0)],
        ),
    )
    .await;
    phases(&a, "apply-1", 42, "apply/staging", &[Phase::APPLYING]).await;
    seed_terminal_status(&a, "apply-1", "success").await;
    let out = get_lifecycle(&a, 42).await;
    let k = by_key(&out);
    assert_eq!(k["apply"]["state"], "done", "{out:?}");
    assert_eq!(k["verify"]["state"], "done", "{out:?}");
}

#[tokio::test]
async fn get_lifecycle_scopes_to_head_sha() {
    let (_d, a) = staging_app();
    seed_init(
        &a,
        init(
            "plan-old",
            "sha1",
            42,
            "plan/staging",
            vec![stack("projects/a", Status::Planned, 0, 0)],
        ),
    )
    .await;
    seed_init(
        &a,
        init(
            "apply-old",
            "sha1",
            42,
            "apply/staging",
            vec![stack("projects/a", Status::Safe, 0, 0)],
        ),
    )
    .await;
    phases(&a, "apply-old", 42, "apply/staging", &[Phase::APPLYING]).await;
    seed_init(
        &a,
        init(
            "plan-new",
            "sha2",
            42,
            "plan/staging",
            vec![stack("projects/a", Status::Pending, 0, 0)],
        ),
    )
    .await;
    phases(&a, "plan-new", 42, "plan/staging", &[Phase::WARMING]).await;
    for (id, ts) in [
        ("plan-old", "2026-07-15 04:00:00"),
        ("apply-old", "2026-07-15 04:10:00"),
        ("plan-new", "2026-07-15 04:20:00"),
    ] {
        set_created(&a, id, ts);
    }
    let out = get_lifecycle(&a, 42).await;
    let k = by_key(&out);
    assert!(k.contains_key("prepare"), "{:?}", keys(&out));
    for key in ["apply", "verify"] {
        assert!(!k.contains_key(key), "{key} from sha1: {:?}", keys(&out));
    }
}

#[tokio::test]
async fn get_lifecycle_unknown_phase_passes_through() {
    let (_d, a) = staging_app();
    seed_init(
        &a,
        Init {
            id: "plan-1".into(),
            pr: 7,
            environment: "staging".into(),
            context: "plan/staging".into(),
            ..Init::default()
        },
    )
    .await;
    phases(&a, "plan-1", 7, "plan/staging", &[Phase::LINTING]).await;
    let out = get_lifecycle(&a, 7).await;
    assert!(keys(&out).iter().any(|k| k == "linting"), "{out:?}");
}
