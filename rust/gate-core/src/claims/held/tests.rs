// Ported case by case from internal/claims/held_test.go.
use crate::claims::decide::tests::{acquired, leases, t0};
use crate::claims::*;

fn stacks(s: &[&str]) -> Vec<String> {
    s.iter().map(|s| s.to_string()).collect()
}

#[test]
fn held_overlap_and_expiry() {
    let s = evolve(ClaimSet::new(), &acquired(9, &["a"], leases(1)));
    // PR 7 wants a and c; a is held by PR 9.
    let v = held(&s, 7, &stacks(&["a", "c"]), t0());
    assert!(v.held);
    assert_eq!(v.blocking, ["a"]);
    // A PR's own claim never blocks it.
    assert!(!held(&s, 9, &stacks(&["a"]), t0()).held);
    // After expiry the claim no longer holds (read time, no event).
    assert!(!held(&s, 7, &stacks(&["a"]), leases(2)).held);
}

#[test]
fn held_multiple_blockers() {
    let s = evolve(ClaimSet::new(), &acquired(9, &["a", "b"], leases(1)));
    let v = held(&s, 7, &stacks(&["b", "a", "c"]), t0());
    assert!(v.held);
    assert_eq!(v.blocking, ["a", "b"]);
}

#[test]
fn held_nil_blocking_when_clear() {
    let v = held(&ClaimSet::new(), 7, &stacks(&["a"]), t0());
    assert!(!v.held);
    assert!(v.blocking.is_empty());
}

#[test]
fn held_reports_blocking_prs() {
    let exp = leases(1);
    let s = ClaimSet::from([
        (
            "stacks/a".into(),
            Claim {
                pr: 9,
                expires_at: exp,
            },
        ),
        (
            "stacks/b".into(),
            Claim {
                pr: 3,
                expires_at: exp,
            },
        ),
        (
            "stacks/c".into(),
            Claim {
                pr: 9,
                expires_at: exp,
            },
        ),
    ]);
    let v = held(&s, 7, &stacks(&["stacks/a", "stacks/b", "stacks/c"]), t0());
    assert!(v.held);
    assert_eq!(v.blocking_prs, [3, 9]);
}
