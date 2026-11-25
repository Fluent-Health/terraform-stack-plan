use super::*;
use crate::uniqueness::Kind;

const TODAY: Date = Date {
    year: 2026,
    month: 7,
    day: 17,
};

fn allow(unit: &str, key: &str, envs: &[&str], reason: &str, expires: &str) -> AllowRule {
    AllowRule {
        unit: unit.into(),
        key: key.into(),
        envs: envs.iter().map(|s| s.to_string()).collect(),
        reason: reason.into(),
        expires: expires.into(),
    }
}

fn vio(unit: &str, key: &str, envs: &[&str], severity: Severity) -> Violation {
    Violation {
        unit: unit.into(),
        key: key.into(),
        value: crate::uniqueness::Value::Nil,
        envs: envs.iter().map(|s| s.to_string()).collect(),
        kind: Kind::Duplicate,
        severity,
    }
}

const V: Severity = Severity::Violation;

// Go: TestAllowMatchesExactKey.
#[test]
fn exact_key() {
    let a = allow(
        "svc/api",
        "client_id",
        &["dev", "prod"],
        "shared by design",
        "",
    );
    assert!(allow_matches(
        &a,
        &vio("svc/api", "client_id", &["dev", "prod"], V),
        TODAY
    ));
}

// Go: TestAllowMatchesGlobKey.
#[test]
fn glob_key() {
    let a = allow("svc/api", "*_url", &["dev", "prod"], "shared by design", "");
    assert!(allow_matches(
        &a,
        &vio("svc/api", "callback_url", &["dev", "prod"], V),
        TODAY
    ));
}

// Go: TestAllowMatchesGlobKeyNoMatch.
#[test]
fn glob_key_no_match() {
    let a = allow("svc/api", "*_url", &["dev", "prod"], "shared by design", "");
    assert!(!allow_matches(
        &a,
        &vio("svc/api", "client_id", &["dev", "prod"], V),
        TODAY
    ));
}

// Go: TestAllowMatchesUnitMismatch.
#[test]
fn unit_mismatch() {
    let a = allow(
        "svc/api",
        "client_id",
        &["dev", "prod"],
        "shared by design",
        "",
    );
    assert!(!allow_matches(
        &a,
        &vio("svc/other", "client_id", &["dev", "prod"], V),
        TODAY
    ));
}

// Go: TestAllowMatchesEnvsSuperset.
#[test]
fn envs_superset() {
    let a = allow(
        "svc/api",
        "client_id",
        &["dev", "staging", "prod"],
        "shared by design",
        "",
    );
    assert!(allow_matches(
        &a,
        &vio("svc/api", "client_id", &["dev", "prod"], V),
        TODAY
    ));
}

// Go: TestAllowMatchesEnvsNotSuperset.
#[test]
fn envs_not_superset() {
    let a = allow("svc/api", "client_id", &["dev"], "shared by design", "");
    assert!(!allow_matches(
        &a,
        &vio("svc/api", "client_id", &["dev", "prod"], V),
        TODAY
    ));
}

// Go: TestAllowMatchesExpired.
#[test]
fn expired() {
    let a = allow(
        "svc/api",
        "client_id",
        &["dev", "prod"],
        "temporary",
        "2020-01-01",
    );
    assert!(!allow_matches(
        &a,
        &vio("svc/api", "client_id", &["dev", "prod"], V),
        TODAY
    ));
}

// Go: TestAllowMatchesExpiresTodayStillMatches.
#[test]
fn expires_today_still_matches() {
    let a = allow(
        "svc/api",
        "client_id",
        &["dev", "prod"],
        "temporary",
        "2026-07-17",
    );
    assert!(allow_matches(
        &a,
        &vio("svc/api", "client_id", &["dev", "prod"], V),
        TODAY
    ));
}

// Go: TestAllowMatchesNoExpiryNeverExpires.
#[test]
fn no_expiry_never_expires() {
    let a = allow("svc/api", "client_id", &["dev", "prod"], "permanent", "");
    let far = Date {
        year: 2099,
        month: 1,
        day: 1,
    };
    assert!(allow_matches(
        &a,
        &vio("svc/api", "client_id", &["dev", "prod"], V),
        far
    ));
}

// Go: TestPartitionJustifiedViolationIsClean.
#[test]
fn justified_violation_is_clean() {
    let (u, s) = partition(
        &[vio("svc/api", "client_id", &["dev", "prod"], V)],
        &[allow(
            "svc/api",
            "client_id",
            &["dev", "prod"],
            "shared by design",
            "",
        )],
        TODAY,
    );
    assert!(u.is_empty() && s.is_empty());
}

// Go: TestPartitionUnjustifiedViolationHasNoAllow.
#[test]
fn unjustified_violation_has_no_allow() {
    let (u, s) = partition(
        &[vio("svc/api", "account_id", &["dev", "prod"], V)],
        &[],
        TODAY,
    );
    assert_eq!(u.len(), 1);
    assert_eq!(u[0].key, "account_id");
    assert!(s.is_empty());
}

// Go: TestPartitionAllowMatchingNothingIsStale.
#[test]
fn allow_matching_nothing_is_stale() {
    let (u, s) = partition(
        &[],
        &[allow(
            "svc/api",
            "nonexistent_id",
            &["dev", "prod"],
            "no longer applies",
            "",
        )],
        TODAY,
    );
    assert!(u.is_empty());
    assert_eq!(s.len(), 1);
    assert_eq!(s[0].key, "nonexistent_id");
}

// Go: TestPartitionExpiredAllowIsNeitherActiveNorStale.
#[test]
fn expired_allow_is_neither_active_nor_stale() {
    let (u, s) = partition(
        &[vio("svc/api", "client_id", &["dev", "prod"], V)],
        &[allow(
            "svc/api",
            "client_id",
            &["dev", "prod"],
            "temporary",
            "2020-01-01",
        )],
        TODAY,
    );
    assert_eq!(u.len(), 1);
    assert!(s.is_empty());
}

// Go: TestPartitionReportOnlyNeverNeedsAllow.
#[test]
fn report_only_never_needs_allow() {
    let (u, s) = partition(
        &[vio(
            "svc/api",
            "client_id",
            &["dev1", "dev2"],
            Severity::ReportOnly,
        )],
        &[],
        TODAY,
    );
    assert!(u.is_empty() && s.is_empty());
}

// Go: TestPartitionFirstMatchingAllowAbsorbsMultipleViolations.
#[test]
fn first_matching_allow_absorbs_multiple() {
    let (u, s) = partition(
        &[
            vio("svc/api", "callback_url", &["dev", "prod"], V),
            vio("svc/api", "webhook_url", &["dev", "prod"], V),
        ],
        &[allow(
            "svc/api",
            "*_url",
            &["dev", "prod"],
            "shared sandbox urls",
            "",
        )],
        TODAY,
    );
    assert!(u.is_empty() && s.is_empty());
}

#[test]
fn dates_parse_strictly_and_from_unix() {
    assert!(Date::parse("2024-02-29").is_some());
    assert!(Date::parse("2023-02-29").is_none());
    assert!(Date::parse("2024-1-01").is_none());
    assert!(Date::parse("garbage").is_none());
    assert!(is_expired("not-a-date", TODAY));
    assert_eq!(
        Date::from_unix(0),
        Date {
            year: 1970,
            month: 1,
            day: 1
        }
    );
    // 2026-07-17T15:30:00Z
    assert_eq!(Date::from_unix(1_784_302_200), TODAY);
}
