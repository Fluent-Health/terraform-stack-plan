// Ported from internal/store/claims_test.go and claims_alias_test.go.
use jiff::{SignedDuration, Timestamp};

use super::*;
use crate::db::tests::temp_store;

fn rows(r: &[(&str, i64, Timestamp)]) -> Vec<(String, ClaimRow)> {
    r.iter()
        .map(|(stack, owner_pr, expires_at)| {
            (
                stack.to_string(),
                ClaimRow {
                    owner_pr: *owner_pr,
                    expires_at: *expires_at,
                },
            )
        })
        .collect()
}

#[test]
fn list_claims() {
    let (_dir, s) = temp_store();
    let now = Timestamp::now();
    let exp = now + SignedDuration::from_mins(30);
    s.replace_claims("prod", &rows(&[("b", 7, exp), ("a", 7, exp)]))
        .unwrap();
    let got = s.list_claims("prod").unwrap();
    assert_eq!(
        got.iter()
            .map(|c| c.stack_path.as_str())
            .collect::<Vec<_>>(),
        ["a", "b"]
    );
    assert_eq!((got[0].owner_pr, got[0].environment.as_str()), (7, "prod"));
    assert_eq!(got[0].expires_at, exp);
    // Every claim, expired or not.
    s.replace_claims(
        "prod",
        &rows(&[
            ("a", 7, exp),
            ("b", 7, exp),
            ("c", 9, now - SignedDuration::from_mins(1)),
        ]),
    )
    .unwrap();
    assert_eq!(s.list_claims("prod").unwrap().len(), 3);
    // Another environment's claims stay out.
    s.replace_claims("stage", &rows(&[("x", 5, exp)])).unwrap();
    assert_eq!(s.list_claims("prod").unwrap().len(), 3);
}

#[test]
fn replace_claims_drops_rows_the_fold_dropped() {
    let (_dir, s) = temp_store();
    let exp = Timestamp::now() + SignedDuration::from_mins(30);
    s.replace_claims("prod", &rows(&[("a", 7, exp), ("b", 7, exp)]))
        .unwrap();
    s.replace_claims("prod", &rows(&[("b", 8, exp)])).unwrap();
    let got = s.list_claims("prod").unwrap();
    assert_eq!(got.len(), 1);
    assert_eq!((got[0].stack_path.as_str(), got[0].owner_pr), ("b", 8));
}

#[test]
fn sweep_expired_claims() {
    let (_dir, s) = temp_store();
    let now = Timestamp::now();
    s.replace_claims(
        "prod",
        &rows(&[("a", 7, now - SignedDuration::from_mins(1))]),
    )
    .unwrap();
    s.replace_claims(
        "stage",
        &rows(&[("b", 8, now + SignedDuration::from_hours(1))]),
    )
    .unwrap();
    assert_eq!(s.sweep_expired_claims(now).unwrap(), ["prod"]);
    let live = s
        .list_claims("stage")
        .unwrap()
        .iter()
        .filter(|c| c.expires_at > now)
        .count();
    assert_eq!(live, 1);
}

#[test]
fn associate_claim_execution() {
    let (_dir, s) = temp_store();
    let exp = Timestamp::now() + SignedDuration::from_hours(1);
    s.replace_claims("prod", &rows(&[("a", 7, exp), ("b", 7, exp)]))
        .unwrap();
    s.associate_claim_execution("prod", 7, "exec-456").unwrap();
    let conn = s.conn();
    let mut stmt = conn
        .prepare(
            "SELECT execution_id FROM apply_claims WHERE environment = 'prod' AND owner_pr = 7",
        )
        .unwrap();
    let ids = stmt
        .query_map([], |r| r.get::<_, String>(0))
        .unwrap()
        .collect::<rusqlite::Result<Vec<_>>>()
        .unwrap();
    assert_eq!(ids, ["exec-456", "exec-456"]);
}

// claims_alias_test.go: the store's claim row is the wire type.
#[test]
fn claim_is_events_type() {
    let (_dir, s) = temp_store();
    let exp = Timestamp::now() + SignedDuration::from_hours(1);
    s.replace_claims("prod", &rows(&[("a", 7, exp)])).unwrap();
    let c: Claim = s.list_claims("prod").unwrap().remove(0);
    assert_eq!(c.owner_pr, 7);
}
