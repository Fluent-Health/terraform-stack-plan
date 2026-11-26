// Ported case by case from internal/claims/evolve_test.go.
use crate::claims::decide::tests::{acquired, leases};
use crate::claims::*;

#[test]
fn evolve_acquire_renew_release() {
    let s = evolve(ClaimSet::new(), &acquired(7, &["a", "b"], leases(1)));
    assert_eq!((s["a"].pr, s["b"].pr), (7, 7));
    let s = evolve(
        s,
        &ClaimRenewed {
            pr: 7,
            expires_at: leases(2),
        }
        .into(),
    );
    assert_eq!(s["a"].expires_at, leases(2));
    let s = evolve(s, &ClaimReleased { pr: 7 }.into());
    assert!(s.is_empty(), "{s:?}");
}

#[test]
fn evolve_release_stack() {
    let s = evolve(ClaimSet::new(), &acquired(7, &["a", "b"], leases(1)));
    let s = evolve(
        s,
        &ClaimStackReleased {
            pr: 7,
            stack: "a".into(),
        }
        .into(),
    );
    assert!(!s.contains_key("a"));
    assert_eq!(s["b"].pr, 7);
}

#[test]
fn evolve_release_stack_wrong_pr() {
    let s = evolve(ClaimSet::new(), &acquired(7, &["a"], leases(1)));
    let s = evolve(
        s,
        &ClaimStackReleased {
            pr: 9,
            stack: "a".into(),
        }
        .into(),
    );
    assert_eq!(s["a"].pr, 7);
}

#[test]
fn evolve_purity() {
    // Evolve takes the ledger by value; a kept copy must stay as it was.
    let base = evolve(ClaimSet::new(), &acquired(7, &["a", "b"], leases(1)));
    let snapshot = base.clone();
    let _ = evolve(
        base.clone(),
        &ClaimRenewed {
            pr: 7,
            expires_at: leases(2),
        }
        .into(),
    );
    let _ = evolve(base.clone(), &ClaimReleased { pr: 7 }.into());
    let _ = evolve(
        base.clone(),
        &ClaimStackReleased {
            pr: 7,
            stack: "a".into(),
        }
        .into(),
    );
    assert_eq!(base, snapshot);
}
