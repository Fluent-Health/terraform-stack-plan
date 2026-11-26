// Ported from internal/server/readhandlers_test.go and prmergestate_test.go.
use std::sync::Arc;

use serde_json::Value;
use tfstackplan_gate::approval::{GrantState, Request};
use tfstackplan_gate::claims::{self, AcquireClaim, Claim, ClaimAcquired, Claims};
use tfstackplan_gate::events::{GateTarget, Init, StackState};
use tfstackplan_gate::reconcile::{
    self, BlockReason, Blocked, Blocker, ChangeSet, Classified, Gate, GateBlocked, GateSatisfied,
    GateState, GrantObserved, Pending, Satisfied, Target,
};
use tfstackplan_server_clients::approval::{Backend as _, Fake};
use tfstackplan_server_clients::github::{MergeQueueEntry, MergeQueueResult, MockGitHub};
use tfstackplan_store::{PrMeta, exec_stream_id};

use crate::testutil::{seed_init, send, store};
use crate::{App, Config};

fn app_over(gh: MockGitHub, cfg: Config) -> (tempfile::TempDir, App) {
    let (dir, s) = store();
    (dir, App::new(s, Arc::new(gh), cfg))
}

async fn get_json(app: &Arc<App>, path: &str) -> Value {
    let w = send(app, "GET", path, "", "").await;
    assert_eq!(w.status, 200, "{path}: {}", w.text());
    serde_json::from_slice(&w.body).unwrap_or_else(|e| panic!("{path}: {e}: {}", w.text()))
}

fn sql(app: &App, q: &str) {
    app.store()
        .exec(|c| c.execute_batch(q))
        .unwrap_or_else(|e| panic!("{q}: {e}"));
}

fn gate_target(class: &str, target: &str) -> GateTarget {
    GateTarget {
        class: class.into(),
        target: target.into(),
    }
}

fn target(class: &str, target: &str, grant_name: &str, grant: GrantState) -> Target {
    Target {
        class: class.into(),
        target: target.into(),
        grant_name: grant_name.into(),
        grant,
    }
}

// --- readhandlers_test.go ---

#[tokio::test]
async fn inspect_grants() {
    let (_d, mut app) = app_over(MockGitHub::default(), Config::default());
    sql(
        &app,
        "INSERT INTO gate_targets (pr, environment, class, target, grant_name, state, requester)
         VALUES (7, 'staging', 'iam', 'proj-a', 'grant-1', 'AWAITING', 'req-1');
         INSERT INTO gate_targets (pr, environment, class, target, grant_name, state, requester)
         VALUES (8, 'prod', 'iam', 'proj-b', 'grant-2', 'ACTIVE', 'req-2');",
    );
    let fake = Arc::new(Fake::new());
    app.approval = Some(fake.clone());
    let app = Arc::new(app);

    let all = get_json(&app, "/api/inspect/grants").await;
    assert_eq!(all.as_array().map(Vec::len), Some(2), "{all}");
    let open = get_json(&app, "/api/inspect/grants?state=open").await;
    assert_eq!(open.as_array().map(Vec::len), Some(2), "{open}");

    let req = Request {
        class: "iam".into(),
        target: "proj-a".into(),
        pr: 7,
        environment: "staging".into(),
        ..Request::default()
    };
    fake.request_grant(&req).await.expect("request grant");
    fake.approve(&req);

    let live = get_json(&app, "/api/inspect/grants?live=1").await;
    let lg = live
        .as_array()
        .expect("array")
        .iter()
        .find(|g| g["pr"] == 7 && g["environment"] == "staging")
        .unwrap_or_else(|| panic!("pr 7 not found in live grants: {live}"));
    assert_eq!(lg["drift_detected"], true, "{lg}");
    assert_eq!(lg["actual_state"], "ACTIVE", "{lg}");
}

#[tokio::test]
async fn inspect_gate() {
    let (_d, app) = app_over(MockGitHub::default(), Config::default());
    let stream = exec_stream_id(7, "staging");
    let s1 = ChangeSet {
        pr: 7,
        environment: "staging".into(),
        gate: GateState::Pending(Pending {
            targets: vec![target("iam", "proj-a", "", GrantState::Awaiting)],
            ..Pending::default()
        }),
        ..ChangeSet::default()
    };
    app.store()
        .append::<Gate>(
            &stream,
            0,
            &[Classified {
                gates: vec![gate_target("iam", "proj-a")],
            }
            .into()],
            &s1,
        )
        .expect("append 1");
    let s2 = ChangeSet {
        gate: GateState::Satisfied(Satisfied {
            targets: vec![target("iam", "proj-a", "grant-1", GrantState::Active)],
            ..Satisfied::default()
        }),
        ..s1
    };
    app.store()
        .append::<Gate>(
            &stream,
            1,
            &[
                GrantObserved {
                    class: "iam".into(),
                    target: "proj-a".into(),
                    name: "grant-1".into(),
                    state: GrantState::Active,
                    ..GrantObserved::default()
                }
                .into(),
                GateSatisfied {}.into(),
            ],
            &s2,
        )
        .expect("append 2");
    let app = Arc::new(app);
    let d = get_json(&app, "/api/inspect/gate/7/staging").await;
    assert_eq!(
        (d["pr"].clone(), d["environment"].clone()),
        (7.into(), "staging".into()),
        "{d}"
    );
    assert_eq!(d["gate_state"], "Satisfied", "{d}");
    let targets = d["targets"].as_array().expect("targets");
    assert!(targets.len() == 1 && targets[0]["state"] == "ACTIVE", "{d}");
    assert!(
        d["reasons"].as_array().map_or(0, Vec::len) >= 2,
        "expected at least 2 reasons: {d}"
    );
}

#[tokio::test]
async fn inspect_claims() {
    let (_d, app) = app_over(MockGitHub::default(), Config::default());
    let expires = jiff::Timestamp::now()
        .checked_add(claims::LEASE)
        .expect("expiry");
    let state: claims::ClaimSet = ["stacks/a", "stacks/b"]
        .into_iter()
        .map(|s| {
            (
                s.to_string(),
                Claim {
                    pr: 7,
                    expires_at: expires,
                },
            )
        })
        .collect();
    app.store()
        .append::<Claims>(
            "env:staging",
            0,
            &[ClaimAcquired {
                pr: 7,
                stacks: vec!["stacks/a".into(), "stacks/b".into()],
                expires_at: expires,
            }
            .into()],
            &state,
        )
        .expect("append");
    let app = Arc::new(app);
    let res = get_json(&app, "/api/inspect/claims/staging").await;
    assert_eq!(res["environment"], "staging", "{res}");
    assert_eq!(res["claims"].as_array().map(Vec::len), Some(2), "{res}");
}

#[tokio::test]
async fn inspect_events() {
    let (_d, app) = app_over(MockGitHub::default(), Config::default());
    let state = ChangeSet {
        pr: 7,
        environment: "staging".into(),
        gate: GateState::Pending(Pending::default()),
        ..ChangeSet::default()
    };
    app.store()
        .append::<Gate>(
            "exec:7:staging",
            0,
            &[Classified {
                gates: vec![gate_target("iam", "proj-a")],
            }
            .into()],
            &state,
        )
        .expect("append");
    let app = Arc::new(app);
    let evs = get_json(&app, "/api/inspect/events/exec:7:staging").await;
    let evs = evs.as_array().expect("array");
    assert_eq!(evs.len(), 1, "{evs:?}");
    assert_eq!(evs[0]["type"], "Classified");
    let after = get_json(&app, "/api/inspect/events/exec:7:staging?after=1").await;
    // Go decodes into a slice, so its nil-slice `null` reads as empty too.
    assert_eq!(after.as_array().map_or(0, Vec::len), 0, "{after}");
}

#[tokio::test]
async fn inspect_overview() {
    let (_d, app) = app_over(
        MockGitHub::default(),
        Config {
            environment: "staging".into(),
            ..Config::default()
        },
    );
    app.store()
        .upsert_pr_meta(&PrMeta {
            repo: "fluent/repo".into(),
            pr: 7,
            title: "Some PR Title".into(),
            body: "PR body".into(),
            author_login: "author".into(),
            head_ref: "branch-a".into(),
            url: "url-a".into(),
            auto_merge: true,
            updated_at: None,
        })
        .expect("pr meta");
    seed_init(
        &app,
        Init {
            id: "exec-123".into(),
            pr: 7,
            environment: "staging".into(),
            repo: "fluent/repo".into(),
            sha: "abcdef".into(),
            ..Init::default()
        },
    )
    .await;
    let app = Arc::new(app);
    let ov = get_json(&app, "/api/inspect/overview").await;
    let ov = ov.as_array().expect("array");
    assert_eq!(ov.len(), 1, "{ov:?}");
    assert_eq!(ov[0]["pr"], 7);
    assert_eq!(ov[0]["repo"], "fluent/repo");
    assert_eq!(ov[0]["meta"]["title"], "Some PR Title", "{:?}", ov[0]);
}

#[tokio::test]
async fn inspect_pool() {
    let (_d, app) = app_over(
        MockGitHub::default(),
        Config {
            environment: "staging".into(),
            requester_pool: vec!["sa-1@fh.com".into(), "sa-2@fh.com".into()],
            ..Config::default()
        },
    );
    sql(
        &app,
        "INSERT INTO gate_targets (pr, environment, class, target, grant_name, state, requester, updated_at)
         VALUES (7, 'staging', 'iam', 'proj-a', 'grant-1', 'ACTIVE', 'sa-1@fh.com', datetime('now', '-10 seconds'))",
    );
    app.store()
        .upsert_pr_meta(&PrMeta {
            repo: "fluent/repo".into(),
            pr: 8,
            title: "Blocked PR".into(),
            ..PrMeta::default()
        })
        .expect("pr meta");
    let state = ChangeSet {
        pr: 8,
        environment: "staging".into(),
        gate: GateState::Blocked(Blocked {
            targets: vec![target("iam", "proj-b", "", GrantState::Awaiting)],
            lease: Default::default(),
            by: Blocker {
                reason: BlockReason::SlotForeign,
                by_pr: 7,
                by_env: "staging".into(),
            },
        }),
        ..ChangeSet::default()
    };
    let evs: Vec<reconcile::Event> = vec![
        Classified {
            gates: vec![gate_target("iam", "proj-b")],
        }
        .into(),
        GateBlocked {
            reason: BlockReason::SlotForeign,
            by_pr: 7,
            by_env: "staging".into(),
        }
        .into(),
    ];
    app.store()
        .append::<Gate>(&exec_stream_id(8, "staging"), 0, &evs, &state)
        .expect("append");
    let app = Arc::new(app);
    let res = get_json(&app, "/api/inspect/pool").await;
    assert_eq!(res["environment"], "staging");
    let slots = res["slots"].as_array().expect("slots");
    assert_eq!(slots.len(), 2, "{res}");
    assert!(
        slots[0]["occupied"] == true
            && slots[0]["requester"] == "sa-1@fh.com"
            && slots[0]["pr"] == 7,
        "slot 0: {}",
        slots[0]
    );
    assert!(
        slots[0]["elapsed_seconds"].as_i64().is_some_and(|s| s >= 5),
        "elapsed: {}",
        slots[0]
    );
    assert!(
        slots[1]["occupied"] == false && slots[1]["requester"] == "sa-2@fh.com",
        "slot 1: {}",
        slots[1]
    );
    let waiting = res["waiting"].as_array().expect("waiting");
    assert_eq!(waiting.len(), 1, "{res}");
    let w = &waiting[0];
    assert!(
        w["pr"] == 8
            && w["reason"] == "slot_foreign_open"
            && w["blocker_pr"] == 7
            && w["blocker_env"] == "staging",
        "{w}"
    );
}

#[tokio::test]
async fn handle_merge_queue() {
    let mut gh = MockGitHub::default();
    gh.merge_queue_fn = Some(Box::new(|repo: String| {
        assert_eq!(repo, "octo/repo");
        Ok(MergeQueueResult {
            branch: "main".into(),
            entries: vec![MergeQueueEntry {
                position: 1,
                pr: 774,
                state: "MERGEABLE".into(),
            }],
        })
    }));
    let (_d, app) = app_over(gh, Config::default());
    sql(
        &app,
        "INSERT INTO executions (id, repo, sha, pr, environment) VALUES ('e1','octo/repo','sha1',774,'prod')",
    );
    let app = Arc::new(app);
    let mq = get_json(&app, "/api/merge-queue").await;
    assert_eq!(mq["branch"], "main", "{mq}");
    let entries = mq["entries"].as_array().expect("entries");
    assert!(entries.len() == 1 && entries[0]["pr"] == 774, "{mq}");
}

#[tokio::test]
async fn handle_merge_queue_degrades_without_executions() {
    let mut gh = MockGitHub::default();
    gh.merge_queue_fn = Some(Box::new(|_| {
        panic!("MergeQueue must not be called when no execution exists")
    }));
    let (_d, app) = app_over(gh, Config::default());
    let app = Arc::new(app);
    let mq = get_json(&app, "/api/merge-queue").await;
    assert_eq!(
        mq["entries"].as_array().map_or(0, Vec::len),
        0,
        "entries want empty: {mq}"
    );
}

// --- prmergestate_test.go ---

fn pr_merge_state_app() -> (tempfile::TempDir, App) {
    app_over(
        MockGitHub::default(),
        Config {
            public_base_url: "https://srv".into(),
            environment: "prod".into(),
            ..Config::default()
        },
    )
}

/// Go's `seedPlan`: an execution `<sha>-<env>` over `stacks`.
async fn seed_plan(app: &App, pr: i64, env: &str, repo: &str, sha: &str, stacks: &[&str]) {
    seed_init(
        app,
        Init {
            id: format!("{sha}-{env}"),
            repo: repo.into(),
            sha: sha.into(),
            pr,
            environment: env.into(),
            stacks: stacks
                .iter()
                .map(|p| StackState {
                    path: (*p).into(),
                    ..StackState::default()
                })
                .collect(),
            ..Init::default()
        },
    )
    .await;
}

#[tokio::test]
async fn pr_merge_state_no_plan() {
    let (_d, app) = pr_merge_state_app();
    let got = app.pr_merge_state(42).await;
    assert_eq!(got.environment, "prod");
    assert_eq!(got.required_check, "apply-lock/prod");
    assert!(got.merge_blocked, "fail-closed with no plan");
    assert!(!got.blocker.is_empty());
    assert_eq!(got.check_conclusion, "");
}

#[tokio::test]
async fn pr_merge_state_clear() {
    let (_d, app) = pr_merge_state_app();
    seed_plan(&app, 7, "prod", "o/r", "sha1", &["a", "b"]).await;
    app.store()
        .set_report("sha1-prod", "# report")
        .expect("report");
    let got = app.pr_merge_state(7).await;
    assert!(!got.merge_blocked);
    assert_eq!(got.blocker, "");
    assert_eq!(got.check_conclusion, "success");
}

#[tokio::test]
async fn pr_merge_state_held() {
    let (_d, app) = pr_merge_state_app();
    seed_plan(&app, 7, "prod", "o/r", "sha1", &["a", "b"]).await;
    app.store()
        .set_report("sha1-prod", "# report")
        .expect("report");
    app.handle_claim(
        "prod",
        AcquireClaim {
            pr: 5,
            stacks: vec!["a".into()],
            now: jiff::Timestamp::now(),
        }
        .into(),
    )
    .await
    .expect("claim");
    let got = app.pr_merge_state(7).await;
    assert!(got.merge_blocked, "held");
    assert!(!got.blocker.is_empty());
    assert_eq!(got.check_conclusion, "");
}
