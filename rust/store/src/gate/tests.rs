// Ported from internal/store/gate_test.go and orphan_test.go.
use rusqlite::params;

use super::*;
use crate::db::tests::temp_store;

/// Writes a `gate_targets` row exactly as the projection does.
/// `row` is class, target, grant name, state, requester.
fn seed_gate_target(s: &Store, pr: i64, environment: &str, row: [&str; 5]) {
    let [class, target, grant, state, requester] = row;
    s.conn()
        .execute(
            "INSERT INTO gate_targets (pr, environment, class, target, grant_name, state, requester)
             VALUES (?1,?2,?3,?4,?5,?6,?7)
             ON CONFLICT(pr, environment, class, target) DO UPDATE SET
               grant_name=excluded.grant_name, state=excluded.state,
               requester=excluded.requester, updated_at=CURRENT_TIMESTAMP",
            params![pr, environment, class, target, grant, state, requester],
        )
        .unwrap();
}

fn seed_exec(s: &Store, id: &str, repo: &str, pr: i64, env: &str) {
    s.conn()
        .execute(
            "INSERT INTO executions (id, repo, sha, pr, environment) VALUES (?1,?2,'sha',?3,?4)",
            params![id, repo, pr, env],
        )
        .unwrap();
}

#[test]
fn migration_008_drops_gate_runs() {
    let (_dir, s) = temp_store();
    let conn = s.conn();
    assert!(
        conn.execute_batch("SELECT count(*) FROM gate_runs")
            .is_err()
    );
    conn.execute_batch("SELECT count(*) FROM gate_targets")
        .unwrap();
}

#[test]
fn migration_009_clears_apply_claims() {
    let (_dir, s) = temp_store();
    s.conn()
        .execute_batch("SELECT count(*) FROM apply_claims")
        .unwrap();
}

#[test]
fn pending_gates() {
    let (_dir, s) = temp_store();
    seed_gate_target(&s, 42, "staging", ["iam", "proj-a", "g1", "ACTIVE", ""]);
    seed_gate_target(&s, 42, "staging", ["iam", "proj-b", "g2", "AWAITING", ""]);
    seed_gate_target(&s, 7, "prod", ["iam", "proj-c", "g3", "ACTIVE", ""]);
    assert_eq!(
        s.pending_gates().unwrap(),
        vec![GateKey {
            pr: 42,
            environment: "staging".into()
        }]
    );
}

#[test]
fn pr_targets() {
    let (_dir, s) = temp_store();
    seed_gate_target(&s, 7, "nonprod", ["iam", "proj-a", "g1", "AWAITING", ""]);
    seed_gate_target(&s, 7, "prod", ["iam", "proj-b", "g2", "ACTIVE", ""]);
    seed_gate_target(&s, 8, "nonprod", ["iam", "proj-a", "g3", "AWAITING", ""]);
    let mut ts = s.pr_targets(7).unwrap();
    ts.sort();
    assert_eq!(ts.len(), 2);
    assert!(ts.iter().all(|t| t.class == "iam"));
    assert_eq!(
        ts.iter()
            .map(|t| t.environment.as_str())
            .collect::<Vec<_>>(),
        ["nonprod", "prod"]
    );
    let ts8 = s.pr_targets(8).unwrap();
    assert_eq!(ts8.len(), 1);
    assert_eq!(ts8[0].target, "proj-a");
}

#[test]
fn targets_for_and_delete_target() {
    let (_dir, s) = temp_store();
    seed_gate_target(
        &s,
        7,
        "nonprod",
        ["iam", "proj-a", "g1", "AWAITING", "requester-bob"],
    );
    assert_eq!(
        s.targets_for(7, "nonprod").unwrap(),
        vec![GateTargetRow {
            class: "iam".into(),
            target: "proj-a".into(),
            grant_name: "g1".into(),
            state: "AWAITING".into(),
            requester: "requester-bob".into(),
        }]
    );
    s.write(|tx| delete_target(tx, 7, "nonprod", "iam", "proj-a"))
        .unwrap();
    assert!(s.targets_for(7, "nonprod").unwrap().is_empty());
}

#[test]
fn open_grant_prs_collapses_multi_env() {
    let (_dir, s) = temp_store();
    seed_exec(&s, "e1", "o/r", 7, "staging");
    seed_gate_target(&s, 7, "staging", ["iam", "p1", "g1", "ACTIVE", ""]);
    seed_gate_target(&s, 7, "prod", ["iam", "p1", "g2", "AWAITING", ""]);
    let got = s.open_grant_prs().unwrap();
    assert_eq!(
        got,
        vec![OpenGrantPr {
            pr: 7,
            repo: "o/r".into()
        }]
    );
}

#[test]
fn open_grant_prs() {
    let (_dir, s) = temp_store();
    // PR 7: fully active, the merged-PR orphan the pending list leaves out.
    seed_exec(&s, "e7", "o/r7", 7, "staging");
    seed_gate_target(&s, 7, "staging", ["iam", "p1", "g1", "ACTIVE", ""]);
    // PR 8: only a revoked grant, so nothing is open.
    seed_exec(&s, "e8", "o/r8", 8, "staging");
    seed_gate_target(&s, 8, "staging", ["iam", "p1", "g2", "REVOKED", ""]);
    // PR 9: awaiting.
    seed_exec(&s, "e9", "o/r9", 9, "prod");
    seed_gate_target(&s, 9, "prod", ["iam", "p2", "g3", "AWAITING", ""]);
    assert_eq!(
        s.open_grant_prs().unwrap(),
        vec![
            OpenGrantPr {
                pr: 7,
                repo: "o/r7".into()
            },
            OpenGrantPr {
                pr: 9,
                repo: "o/r9".into()
            },
        ]
    );
}

#[test]
fn pending_approvals_lists_every_non_active_target() {
    let (_dir, s) = temp_store();
    seed_exec(&s, "e1", "o/r", 7, "staging");
    seed_gate_target(&s, 7, "staging", ["iam", "p1", "g1", "ACTIVE", ""]);
    seed_gate_target(&s, 7, "staging", ["iam", "p2", "g2", "DENIED", "sa"]);
    let got = s.pending_approvals().unwrap();
    assert_eq!(got.len(), 1);
    assert_eq!(
        (
            got[0].repo.as_str(),
            got[0].target.as_str(),
            got[0].state.as_str()
        ),
        ("o/r", "p2", "DENIED")
    );
}
