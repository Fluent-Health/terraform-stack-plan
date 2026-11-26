use std::collections::HashSet;

use super::*;

#[test]
fn codes_are_unique_and_well_formed() {
    let re = regex::Regex::new(r"^[A-Z]+-[0-9]{3}$").unwrap();
    let mut seen = HashSet::new();
    for c in all() {
        assert!(re.is_match(c.0), "{}", c.0);
        assert!(seen.insert(c), "duplicate {}", c.0);
    }
    assert!(!all().is_empty());
}

#[test]
fn error_carries_code() {
    let err = error(
        UNKNOWN_STATUS,
        format!("unknown stack status {}", crate::gofmt::quote("bogus")),
    );
    assert_eq!(err.to_string(), r#"WIRE-001: unknown stack status "bogus""#);
    assert_eq!(err.code, UNKNOWN_STATUS);
}

#[test]
fn unknown_status_registered() {
    assert!(all().contains(&UNKNOWN_STATUS));
}
