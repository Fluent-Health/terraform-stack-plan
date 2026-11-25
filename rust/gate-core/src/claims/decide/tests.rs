// Ported case by case from internal/claims/decide_test.go.
use jiff::Timestamp;

use crate::claims::*;

pub(crate) fn t0() -> Timestamp {
    "2026-06-24T12:00:00Z".parse().unwrap()
}

/// `t0` plus `n` leases.
pub(crate) fn leases(n: i32) -> Timestamp {
    t0() + LEASE * n
}

pub(crate) fn acquired(pr: i64, stacks: &[&str], expires_at: Timestamp) -> Event {
    ClaimAcquired {
        pr,
        stacks: stacks.iter().map(|s| s.to_string()).collect(),
        expires_at,
    }
    .into()
}

#[test]
fn decide_acquire_computes_expiry() {
    let got = decide(
        &ClaimSet::new(),
        &AcquireClaim {
            pr: 7,
            stacks: vec!["a".into(), "b".into()],
            now: t0(),
        }
        .into(),
    );
    assert_eq!(got, vec![acquired(7, &["a", "b"], leases(1))]);
}

#[test]
fn decide_renew_computes_expiry() {
    let s = evolve(ClaimSet::new(), &acquired(7, &["a"], leases(1)));
    let got = decide(&s, &RenewClaim { pr: 7, now: t0() }.into());
    assert_eq!(
        got,
        vec![
            ClaimRenewed {
                pr: 7,
                expires_at: leases(1)
            }
            .into()
        ]
    );
}

#[test]
fn decide_release() {
    let s = evolve(ClaimSet::new(), &acquired(7, &["a"], leases(1)));
    assert_eq!(
        decide(&s, &ReleaseClaim { pr: 7 }.into()),
        vec![ClaimReleased { pr: 7 }.into()]
    );
}

#[test]
fn decide_release_stack() {
    let s = evolve(ClaimSet::new(), &acquired(7, &["a", "b"], leases(1)));
    assert_eq!(
        decide(
            &s,
            &ReleaseClaimStack {
                pr: 7,
                stack: "a".into()
            }
            .into()
        ),
        vec![
            ClaimStackReleased {
                pr: 7,
                stack: "a".into()
            }
            .into()
        ]
    );
}
