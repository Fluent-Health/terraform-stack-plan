use std::collections::{BTreeMap, BTreeSet};

use super::*;
use crate::uniqueness::classify::{
    default_key_patterns, default_quantity_suffixes, default_value_patterns,
};

fn default_classifier() -> Classifier {
    Classifier {
        val_pats: default_value_patterns(),
        key_pats: default_key_patterns(),
        qty_suffixes: default_quantity_suffixes(),
        ..Default::default()
    }
}

fn unit(id: &str, inputs: &[(&str, &[(&str, Value)])]) -> Unit {
    let mut u = Unit {
        id: id.into(),
        ..Default::default()
    };
    for (env, leaves) in inputs {
        u.envs.push(env.to_string());
        u.inputs.insert(
            env.to_string(),
            leaves
                .iter()
                .map(|(k, v)| (k.to_string(), v.clone()))
                .collect(),
        );
    }
    u
}

fn tiers(t: &[(&str, &str)]) -> BTreeMap<String, Tier> {
    t.iter()
        .map(|(e, t)| (e.to_string(), t.to_string()))
        .collect()
}

fn vio(
    unit: &str,
    key: &str,
    value: Value,
    envs: &[&str],
    kind: Kind,
    severity: Severity,
) -> Violation {
    Violation {
        unit: unit.into(),
        key: key.into(),
        value,
        envs: envs.iter().map(|s| s.to_string()).collect(),
        kind,
        severity,
    }
}

fn token_classifier() -> Classifier {
    Classifier::new(
        vec![],
        vec![],
        vec![],
        BTreeMap::from([
            ("dev".to_string(), vec!["acme-dev".to_string()]),
            ("prod".to_string(), vec!["acme-prod".to_string()]),
        ]),
        BTreeSet::new(),
    )
}

// Go: TestFindDuplicatesCrossBoundaryIdentifierViolation.
#[test]
fn cross_boundary_identifier_is_violation() {
    let shared = Value::str("acme-shared-project");
    let u = unit(
        "app-dev",
        &[
            ("dev", &[("project_id", shared.clone())]),
            ("prod", &[("project_id", shared.clone())]),
        ],
    );
    let got = find_duplicates(
        &u,
        &tiers(&[("dev", "nonprod"), ("prod", "prod")]),
        "prod",
        &default_classifier(),
    );
    assert_eq!(
        got,
        vec![vio(
            "app-dev",
            "project_id",
            shared,
            &["dev", "prod"],
            Kind::Duplicate,
            Severity::Violation
        )]
    );
}

// Go: TestFindDuplicatesWithinUnprotectedIsReportOnly.
#[test]
fn within_unprotected_is_report_only() {
    let v = Value::str("acme-client-1234");
    let u = unit(
        "app-dev",
        &[
            ("dev1", &[("client_id", v.clone())]),
            ("dev2", &[("client_id", v.clone())]),
        ],
    );
    let got = find_duplicates(
        &u,
        &tiers(&[("dev1", "nonprod"), ("dev2", "nonprod")]),
        "prod",
        &default_classifier(),
    );
    assert_eq!(
        got,
        vec![vio(
            "app-dev",
            "client_id",
            v,
            &["dev1", "dev2"],
            Kind::Duplicate,
            Severity::ReportOnly
        )]
    );
}

// Go: TestFindDuplicatesWithinProtectedIsReportOnly.
#[test]
fn within_protected_is_report_only() {
    let v = Value::str("acme-client-1234");
    let u = unit(
        "app-dev",
        &[
            ("prod1", &[("client_id", v.clone())]),
            ("prod2", &[("client_id", v.clone())]),
        ],
    );
    let got = find_duplicates(
        &u,
        &tiers(&[("prod1", "prod"), ("prod2", "prod")]),
        "prod",
        &default_classifier(),
    );
    assert_eq!(
        got,
        vec![vio(
            "app-dev",
            "client_id",
            v,
            &["prod1", "prod2"],
            Kind::Duplicate,
            Severity::ReportOnly
        )]
    );
}

// Go: TestFindDuplicatesBoolNeverFlagged.
#[test]
fn bool_never_flagged() {
    let u = unit(
        "app-dev",
        &[
            ("dev", &[("enabled", Value::Bool(true))]),
            ("prod", &[("enabled", Value::Bool(true))]),
        ],
    );
    assert!(
        find_duplicates(
            &u,
            &tiers(&[("dev", "nonprod"), ("prod", "prod")]),
            "prod",
            &default_classifier()
        )
        .is_empty()
    );
}

// Go: TestFindDuplicatesListOfUUIDsCrossBoundary.
#[test]
fn list_of_uuids_cross_boundary() {
    let list = Value::list(&[
        "11111111-1111-1111-1111-111111111111",
        "22222222-2222-2222-2222-222222222222",
    ]);
    let u = unit(
        "app-dev",
        &[
            ("dev", &[("allowed_ids", list.clone())]),
            ("prod", &[("allowed_ids", list.clone())]),
        ],
    );
    let got = find_duplicates(
        &u,
        &tiers(&[("dev", "nonprod"), ("prod", "prod")]),
        "prod",
        &default_classifier(),
    );
    assert_eq!(
        got,
        vec![vio(
            "app-dev",
            "allowed_ids",
            list,
            &["dev", "prod"],
            Kind::Duplicate,
            Severity::Violation
        )]
    );
}

// Go: TestFindDuplicatesMissingTierFailsClosedToProtected.
#[test]
fn missing_tier_fails_closed_to_protected() {
    let v = Value::str("acme-shared-project");
    let u = unit(
        "app-dev",
        &[
            ("dev", &[("project_id", v.clone())]),
            ("unknown-env", &[("project_id", v.clone())]),
        ],
    );
    let got = find_duplicates(
        &u,
        &tiers(&[("dev", "nonprod")]),
        "prod",
        &default_classifier(),
    );
    assert_eq!(
        got,
        vec![vio(
            "app-dev",
            "project_id",
            v,
            &["dev", "unknown-env"],
            Kind::Duplicate,
            Severity::Violation
        )]
    );
}

// Go: TestFindDuplicatesSkipsEnvScopedKeys.
#[test]
fn duplicates_skip_env_scoped_keys() {
    let v = Value::str("acme-shared-project");
    let u = unit(
        "app-dev",
        &[
            ("dev", &[("environments.dev.project_id", v.clone())]),
            ("prod", &[("environments.dev.project_id", v)]),
        ],
    );
    let mut c = default_classifier();
    c.scoped_segs = BTreeSet::from(["dev".to_string()]);
    assert!(
        find_duplicates(
            &u,
            &tiers(&[("dev", "nonprod"), ("prod", "prod")]),
            "prod",
            &c
        )
        .is_empty()
    );
}

// Go: TestFindEnvTokensForeignTokenInProtectedValue.
#[test]
fn foreign_token_in_protected_value() {
    let v = Value::str("https://api.acme-dev.example.com");
    let u = unit("app", &[("prod", &[("api_url", v.clone())])]);
    assert_eq!(
        find_env_tokens(&u, &token_classifier()),
        vec![vio(
            "app",
            "api_url",
            v,
            &["prod"],
            Kind::EnvToken,
            Severity::Violation
        )]
    );
}

// Go: TestFindEnvTokensOwnTokenNotFlagged.
#[test]
fn own_token_not_flagged() {
    let u = unit(
        "app",
        &[(
            "prod",
            &[("api_url", Value::str("https://api.acme-prod.example.com"))],
        )],
    );
    assert!(find_env_tokens(&u, &token_classifier()).is_empty());
}

// Go: TestFindEnvTokensBoundarySafeNoFalseMatch.
#[test]
fn token_match_is_boundary_safe() {
    let u = unit(
        "app",
        &[("prod", &[("name", Value::str("acme-development"))])],
    );
    assert!(find_env_tokens(&u, &token_classifier()).is_empty());
}

// Go: TestFindEnvTokensListElemMatch.
#[test]
fn token_in_a_list_element() {
    let list = Value::list(&["safe-host.example.com", "acme-dev.example.com"]);
    let u = unit("app", &[("prod", &[("allowed_hosts", list.clone())])]);
    assert_eq!(
        find_env_tokens(&u, &token_classifier()),
        vec![vio(
            "app",
            "allowed_hosts",
            list,
            &["prod"],
            Kind::EnvToken,
            Severity::Violation
        )]
    );
}

// Go: TestFindEnvTokensSkipsEnvScopedKeys.
#[test]
fn env_tokens_skip_env_scoped_keys() {
    let mut c = token_classifier();
    c.scoped_segs = BTreeSet::from(["environments".to_string()]);
    let u = unit(
        "app",
        &[(
            "prod",
            &[(
                "environments.dev.api_url",
                Value::str("https://api.acme-dev.example.com"),
            )],
        )],
    );
    assert!(find_env_tokens(&u, &c).is_empty());
}

#[test]
fn group_keys_separate_lists_from_scalars() {
    assert_ne!(group_key(&Value::list(&["a"])), group_key(&Value::str("a")));
    assert_eq!(group_key(&Value::Int(1)), group_key(&Value::str("1")));
}
