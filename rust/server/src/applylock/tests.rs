// Ported from internal/server: applylock_test.go and applylock_release_test.go.
//
// Go's `recordingGitHub` is a MockGitHub whose check runs are id 99
// (`recording_github`); its `lastUpdate`/`updateCount` are read from the mock's
// recorded calls, and "resetting" `lastUpdate` is remembering how many updates
// were already seen.
use std::sync::{Arc, Mutex};

use jiff::Timestamp;
use tfstackplan_gate::claims::{self, AcquireClaim, ReleaseClaim};
use tfstackplan_gate::events::{Finalize, GateRevoke, Init, StackState, Status};
use tfstackplan_server_clients::github::{CheckRunUpdate, MockGitHub};
use tfstackplan_store::ApplyLockCheck;

use crate::render::{ApplyLockVerdict, lock_blocked, lock_section, lock_title};
use crate::testutil::{
    active_claimed_stacks, check_updates, post_json, recording_github, seed_init,
};
use crate::{App, Config};

struct LockApp {
    _dir: tempfile::TempDir,
    a: Arc<App>,
    gh: Arc<MockGitHub>,
    /// `None` reads the wall clock, as Go's `time.Now`.
    clock: Arc<Mutex<Option<Timestamp>>>,
}

impl LockApp {
    fn set_now(&self, t: Timestamp) {
        *self.clock.lock().expect("clock") = Some(t);
    }

    /// Go's `gh.lastUpdate`, counting only updates after the first `since`.
    fn last_update(&self, since: usize) -> CheckRunUpdate {
        check_updates(&self.gh)
            .into_iter()
            .skip(since)
            .last()
            .unwrap_or_default()
    }

    fn update_count(&self) -> usize {
        check_updates(&self.gh).len()
    }
}

/// Go's `newApplyLockTestApp` (and the `Environment: "prod"` variant).
fn lock_app_with(gh: MockGitHub, environment: &str) -> LockApp {
    let (dir, s) = crate::testutil::store();
    let gh = Arc::new(gh);
    let clock: Arc<Mutex<Option<Timestamp>>> = Arc::default();
    let mut a = App::new(
        s,
        gh.clone(),
        Config {
            public_base_url: "https://srv".into(),
            environment: environment.into(),
            ..Config::default()
        },
    );
    let c = Arc::clone(&clock);
    a.now = Arc::new(move || {
        c.lock()
            .ok()
            .and_then(|t| *t)
            .unwrap_or_else(Timestamp::now)
    });
    LockApp {
        _dir: dir,
        a: Arc::new(a),
        gh,
        clock,
    }
}

fn lock_app() -> LockApp {
    lock_app_with(recording_github(), "")
}

fn merge_group_gh(prs: Vec<i64>) -> MockGitHub {
    let mut gh = recording_github();
    gh.merge_group_prs_fn = Some(Box::new(move |_| Ok(prs.clone())));
    gh
}

fn head_sha_gh(sha: &'static str) -> MockGitHub {
    let mut gh = recording_github();
    gh.pr_head_sha_fn = Some(Box::new(move |_| Ok(sha.to_string())));
    gh
}

fn strs(s: &[&str]) -> Vec<String> {
    s.iter().map(|x| (*x).to_string()).collect()
}

/// Go's `seedPlan`: an execution with `stacks` so prChangedStacks finds a plan.
async fn seed_plan(a: &App, pr: i64, env: &str, repo: &str, head_sha: &str, stacks: &[&str]) {
    seed_init(
        a,
        Init {
            id: format!("{head_sha}-{env}"),
            repo: repo.into(),
            sha: head_sha.into(),
            pr,
            environment: env.into(),
            stacks: stacks
                .iter()
                .map(|s| StackState {
                    path: (*s).into(),
                    ..StackState::default()
                })
                .collect(),
            ..Init::default()
        },
    )
    .await;
}

async fn acquire(a: &App, env: &str, pr: i64, stacks: &[&str], now: Timestamp) {
    a.handle_claim(
        env,
        AcquireClaim {
            pr,
            stacks: strs(stacks),
            now,
        }
        .into(),
    )
    .await
    .expect("acquire");
}

#[tokio::test]
async fn claims_endpoints() {
    let l = lock_app();
    acquire(&l.a, "prod", 7, &["a", "b"], Timestamp::now()).await;
    l.a.admin_release_claims("prod", 7, "a").await;
    let got = l.a.store().list_claims("prod").expect("list");
    assert_eq!(got.len(), 1, "after single-stack release: {got:?}");
    assert_eq!(got[0].stack_path, "b");
    l.a.admin_release_claims("prod", 7, "").await;
    let got2 = l.a.store().list_claims("prod").expect("list");
    assert!(got2.is_empty(), "after pr-level release: {got2:?}");
}

#[tokio::test]
async fn post_apply_lock() {
    let l = lock_app();
    let v = ApplyLockVerdict {
        state: "held".into(),
        blocking: strs(&["a"]),
        reason: "x".into(),
        ..ApplyLockVerdict::default()
    };
    l.a.post_apply_lock(
        "o/r",
        "prod",
        "sha1",
        7,
        Some(strs(&["a"])),
        "merge_group",
        &v,
    )
    .await
    .expect("post");
    assert_eq!(l.last_update(0).conclusion, "", "held leaves no conclusion");
    let rec =
        l.a.store()
            .get_apply_lock_check("prod", "sha1")
            .expect("get")
            .expect("record");
    assert_eq!(rec.state, "held");
    let clear = ApplyLockVerdict {
        state: "clear".into(),
        ..ApplyLockVerdict::default()
    };
    let _ =
        l.a.post_apply_lock(
            "o/r",
            "prod",
            "sha1",
            7,
            Some(strs(&["a"])),
            "merge_group",
            &clear,
        )
        .await;
    assert_eq!(l.last_update(0).conclusion, "success");
}

#[tokio::test]
async fn merge_group_held_then_claim() {
    let l = lock_app_with(merge_group_gh(vec![7]), "");
    seed_plan(&l.a, 7, "prod", "o/r", "headPR", &["a", "b"]).await;
    acquire(&l.a, "prod", 5, &["a"], Timestamp::now()).await;
    l.a.handle_merge_group("o/r", "mgsha", "", "checks_requested")
        .await
        .expect("merge group");
    assert_eq!(l.last_update(0).conclusion, "", "overlap must be held");
    l.a.handle_claim("prod", ReleaseClaim { pr: 5 }.into())
        .await
        .expect("release");
    let mark = l.update_count();
    l.a.handle_merge_group("o/r", "mgsha", "", "checks_requested")
        .await
        .expect("merge group");
    assert_eq!(l.last_update(mark).conclusion, "success");
    let c = active_claimed_stacks(l.a.store(), "prod", Timestamp::now());
    assert_eq!(
        (c.get("a"), c.get("b")),
        (Some(&7), Some(&7)),
        "greenlight did not claim: {c:?}"
    );
}

#[tokio::test]
async fn merge_group_no_stack_pr_posts_green_gate() {
    let l = lock_app_with(merge_group_gh(vec![7]), "prod");
    l.a.handle_merge_group("o/r", "mgsha", "", "checks_requested")
        .await
        .expect("merge group");
    assert_ne!(l.update_count(), 0, "no merge-gate check posted");
    assert_eq!(l.last_update(0).conclusion, "success");
    assert!(
        l.a.store()
            .get_apply_lock_check("prod", "mgsha")
            .expect("get")
            .is_some(),
        "merge-gate check not persisted for prod"
    );
}

#[tokio::test]
async fn merge_group_resolves_pr_from_head_ref() {
    let l = lock_app_with(recording_github(), "prod");
    seed_plan(&l.a, 7, "prod", "o/r", "mgsha", &["a"]).await;
    acquire(&l.a, "prod", 5, &["a"], Timestamp::now()).await;
    l.a.handle_merge_group(
        "o/r",
        "mgsha",
        "refs/heads/gh-readonly-queue/main/pr-7-deadbeef",
        "checks_requested",
    )
    .await
    .expect("merge group");
    assert_ne!(l.update_count(), 0, "PR not resolved from head ref");
    assert_eq!(
        l.last_update(0).conclusion,
        "",
        "PR 7's overlap must be held"
    );
}

#[tokio::test]
async fn merge_group_pr_resolution_error() {
    let mut gh = recording_github();
    gh.merge_group_prs_fn = Some(Box::new(|_| {
        Err(tfstackplan_server_clients::Error::msg("github unavailable"))
    }));
    let l = lock_app_with(gh, "");
    assert!(
        l.a.handle_merge_group("o/r", "mgsha", "", "checks_requested")
            .await
            .is_err()
    );
    assert_eq!(l.update_count(), 0, "no check may be posted");
}

#[tokio::test]
async fn pr_apply_lock_evaluate_and_claim() {
    let l = lock_app_with(head_sha_gh("prhead"), "");
    seed_plan(&l.a, 7, "prod", "o/r", "prhead", &["a"]).await;
    l.a.handle_pr_apply_lock("o/r", 7, false).await;
    assert_eq!(l.last_update(0).conclusion, "success");
    l.a.handle_pr_apply_lock("o/r", 7, true).await;
    let c = active_claimed_stacks(l.a.store(), "prod", Timestamp::now());
    assert_eq!(c.get("a"), Some(&7), "merged did not claim: {c:?}");
}

#[tokio::test]
async fn apply_finalize_releases_claims() {
    let l = lock_app();
    acquire(&l.a, "prod", 7, &["a"], Timestamp::now()).await;
    l.a.release_apply_claims("prod", 7).await;
    assert!(active_claimed_stacks(l.a.store(), "prod", Timestamp::now()).is_empty());
    assert!(l.a.store().load_claims("prod").expect("load").is_empty());
}

#[tokio::test]
async fn sweep_claims_once_releases_and_reevaluates() {
    let l = lock_app();
    seed_plan(&l.a, 7, "prod", "o/r", "mgsha", &["a"]).await;
    acquire(
        &l.a,
        "prod",
        5,
        &["a"],
        Timestamp::now() - claims::LEASE * 2,
    )
    .await;
    l.a.store()
        .upsert_apply_lock_check(&ApplyLockCheck {
            environment: "prod".into(),
            head_sha: "mgsha".into(),
            check_run_id: 1,
            pr: 7,
            repo: "o/r".into(),
            stacks: Some(strs(&["a"])),
            state: "held".into(),
            kind: "merge_group".into(),
            execution_id: String::new(),
        })
        .expect("upsert");
    l.a.sweep_claims_once().await;
    assert_eq!(
        l.last_update(0).conclusion,
        "success",
        "sweep did not clear the held check"
    );
}

#[tokio::test]
async fn sweep_expired_claims_uses_injected_clock() {
    let l = lock_app();
    let base: Timestamp = "2026-01-01T12:00:00Z".parse().expect("time");
    l.set_now(base);
    acquire(&l.a, "nonprod", 1, &["a"], base).await;

    let t1 = base + jiff::SignedDuration::from_secs(1);
    l.set_now(t1);
    l.a.sweep_claims_once().await;
    let c = active_claimed_stacks(l.a.store(), "nonprod", t1);
    assert_eq!(c.get("a"), Some(&1), "claim kept before expiry: {c:?}");

    let t2 = base + jiff::SignedDuration::from_hours(24);
    l.set_now(t2);
    l.a.sweep_claims_once().await;
    let c = active_claimed_stacks(l.a.store(), "nonprod", t2);
    assert!(c.is_empty(), "claim gone after expiry: {c:?}");
}

#[tokio::test]
async fn post_plan_apply_lock_on_finalize() {
    let l = lock_app();
    seed_plan(&l.a, 7, "prod", "o/r", "sha7", &["a", "b"]).await;
    let e =
        l.a.store()
            .get_execution("sha7-prod")
            .expect("get")
            .expect("row");
    l.a.post_plan_apply_lock(&e).await;
    assert_eq!(l.last_update(0).conclusion, "success");
    let rec =
        l.a.store()
            .get_apply_lock_check("prod", "sha7")
            .expect("get")
            .expect("applylock_checks not persisted on sha7");
    assert_eq!(rec.pr, 7);
    acquire(&l.a, "prod", 9, &["a"], Timestamp::now()).await;
    let mark = l.update_count();
    l.a.post_plan_apply_lock(&e).await;
    assert_eq!(l.last_update(mark).conclusion, "", "overlap must be held");
}

#[tokio::test]
async fn pr_apply_lock_unverifiable() {
    let l = lock_app_with(head_sha_gh("unplanned_sha"), "");
    l.a.handle_pr_apply_lock("o/r", 123, false).await;
    assert_eq!(l.last_update(0).conclusion, "");
}

#[tokio::test]
async fn merge_group_unverifiable() {
    let l = lock_app_with(merge_group_gh(vec![123]), "");
    l.a.handle_merge_group("o/r", "mgsha", "", "checks_requested")
        .await
        .expect("merge group");
    assert_eq!(l.last_update(0).conclusion, "");
}

#[test]
fn lock_render_pieces() {
    let held = ApplyLockVerdict {
        state: "held".into(),
        blocking: strs(&["stacks/a", "stacks/b"]),
        blocking_prs: vec![3],
        ..ApplyLockVerdict::default()
    };
    assert_eq!(lock_title(&held), "waiting on PR #3's apply");
    let multi = ApplyLockVerdict {
        state: "held".into(),
        blocking: strs(&["stacks/a"]),
        blocking_prs: vec![3, 9],
        ..ApplyLockVerdict::default()
    };
    assert_eq!(lock_title(&multi), "waiting on PRs #3, #9 applies");
    let unverifiable = ApplyLockVerdict {
        state: "unverifiable".into(),
        reason: "claim-ledger load failed".into(),
        ..ApplyLockVerdict::default()
    };
    assert_eq!(
        lock_title(&unverifiable),
        "merge-lock unverifiable — retrying"
    );
    let clear = ApplyLockVerdict {
        state: "clear".into(),
        ..ApplyLockVerdict::default()
    };
    assert_eq!(lock_section(&clear), "");
    assert_eq!(lock_section(&ApplyLockVerdict::default()), "");
    let sec = lock_section(&held);
    assert!(
        sec.contains("stacks/a") && sec.contains("#3"),
        "lockSection = {sec:?}"
    );
    assert!(lock_blocked(&held));
    assert!(!lock_blocked(&clear));
    assert!(!lock_blocked(&ApplyLockVerdict::default()));
}

#[tokio::test]
async fn post_apply_lock_unverifiable_direct() {
    let l = lock_app();
    l.a.post_apply_lock_unverifiable("o/r", "prod", "sha", 123, "pr_head", "reason-text".into())
        .await;
    assert_eq!(l.last_update(0).conclusion, "");
}

// --- applylock_release_test.go ---

fn stack(path: &str, status: Status) -> StackState {
    StackState {
        path: path.into(),
        status,
        ..StackState::default()
    }
}

#[tokio::test]
async fn apply_end_gate_revoke_releases_claim() {
    let l = lock_app();
    // classifyGate: a gateless plan leaves the change set Clean.
    post_json(
        &l.a,
        "/api/init",
        &Init {
            id: "plan-1".into(),
            repo: "o/r".into(),
            sha: "sha".into(),
            pr: 7,
            environment: "staging".into(),
            stacks: vec![stack("a", Status::Planned)],
            ..Init::default()
        },
    )
    .await;
    post_json(
        &l.a,
        "/api/finalize",
        &Finalize {
            id: "plan-1".into(),
            report_markdown: "# r".into(),
            ..Finalize::default()
        },
    )
    .await;
    let now = l.a.now();
    acquire(&l.a, "staging", 7, &["a"], now).await;
    let c = active_claimed_stacks(l.a.store(), "staging", now);
    assert_eq!(c.get("a"), Some(&7), "claim seeded via the ledger: {c:?}");
    let code = post_json(
        &l.a,
        "/api/gate/revoke",
        &GateRevoke {
            pr: 7,
            environment: "staging".into(),
        },
    )
    .await;
    assert_eq!(code, 200);
    let c = active_claimed_stacks(l.a.store(), "staging", l.a.now());
    assert!(
        c.is_empty(),
        "apply-end GateRevoke did not release the claim: {c:?}"
    );
}

#[tokio::test]
async fn classify_pass_finalize_keeps_claim() {
    let l = lock_app();
    post_json(
        &l.a,
        "/api/init",
        &Init {
            id: "apply-1".into(),
            repo: "o/r".into(),
            sha: "sha".into(),
            pr: 7,
            environment: "staging".into(),
            context: "apply/staging".into(),
            stacks: vec![stack("a", Status::Pending)],
            ..Init::default()
        },
    )
    .await;
    acquire(&l.a, "staging", 7, &["a"], l.a.now()).await;
    let code = post_json(
        &l.a,
        "/api/finalize",
        &Finalize {
            id: "apply-1".into(),
            report_markdown: "# r".into(),
            ..Finalize::default()
        },
    )
    .await;
    assert_eq!(code, 200);
    assert!(
        !active_claimed_stacks(l.a.store(), "staging", l.a.now()).is_empty(),
        "classify-pass finalize released the claim before the apply ran"
    );
}
