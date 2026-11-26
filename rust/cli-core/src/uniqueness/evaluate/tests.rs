use super::*;
use crate::config::{AllowBlock, SourceBlock};
use crate::uniqueness::Value;

const NOW: Date = Date {
    year: 2026,
    month: 7,
    day: 17,
};

fn env(name: &str, tier: &str) -> EnvBlock {
    EnvBlock {
        name: name.into(),
        tier: tier.into(),
    }
}

fn unit(id: &str, inputs: &[(&str, &[(&str, &str)])]) -> Unit {
    let mut u = Unit {
        id: id.into(),
        ..Default::default()
    };
    for (e, leaves) in inputs {
        u.envs.push(e.to_string());
        u.inputs.insert(
            e.to_string(),
            leaves
                .iter()
                .map(|(k, v)| (k.to_string(), Value::str(v)))
                .collect(),
        );
    }
    u
}

fn declared_tier_config() -> EnvUniquenessConfig {
    EnvUniquenessConfig {
        protected_tier: "prod".into(),
        environments: vec![
            env("dev", "nonprod"),
            env("staging", "nonprod"),
            env("prod", "prod"),
        ],
        allows: vec![
            AllowBlock {
                unit: "svc/api-a".into(),
                key: "client_id".into(),
                envs: vec!["dev".into(), "prod".into()],
                reason: "shared sandbox project by design".into(),
                expires: String::new(),
            },
            AllowBlock {
                unit: "svc/api-a".into(),
                key: "nonexistent_id".into(),
                envs: vec!["dev".into(), "prod".into()],
                reason: "no longer applies to anything".into(),
                expires: String::new(),
            },
        ],
        ..Default::default()
    }
}

fn eval_units() -> Vec<Unit> {
    vec![
        unit(
            "svc/api-a",
            &[
                (
                    "dev",
                    &[
                        ("client_id", "acme-shared-client"),
                        ("app_id", "acme-nonprod-app"),
                    ],
                ),
                ("staging", &[("app_id", "acme-nonprod-app")]),
                ("prod", &[("client_id", "acme-shared-client")]),
            ],
        ),
        unit(
            "svc/api-b",
            &[
                ("dev", &[("account_id", "acme-shared-account")]),
                ("staging", &[]),
                ("prod", &[("account_id", "acme-shared-account")]),
            ],
        ),
    ]
}

fn tier_input(leaf: &str) -> EnvUniquenessConfig {
    EnvUniquenessConfig {
        protected_tier: "prod".into(),
        source: SourceBlock {
            tier_input: leaf.into(),
            ..Default::default()
        },
        ..Default::default()
    }
}

// Go: TestEvaluateJustifiedUnjustifiedStaleReportOnly.
#[test]
fn justified_unjustified_stale_report_only() {
    let r = evaluate(&declared_tier_config(), &eval_units(), NOW).unwrap();
    assert_eq!(r.unjustified.len(), 1);
    assert_eq!(
        (
            r.unjustified[0].unit.as_str(),
            r.unjustified[0].key.as_str()
        ),
        ("svc/api-b", "account_id")
    );
    assert_eq!(r.stale.len(), 1);
    assert_eq!(r.stale[0].key, "nonexistent_id");
    assert!(
        r.report_only
            .iter()
            .any(|v| v.unit == "svc/api-a" && v.key == "app_id")
    );
    assert!(r.unjustified.iter().all(|v| v.key != "app_id"));
}

// Go: TestEvaluateTierFromSourceInput.
#[test]
fn tier_from_source_input() {
    let units = vec![unit(
        "svc/api-a",
        &[
            (
                "dev",
                &[
                    ("tier_class", "nonprod"),
                    ("client_id", "acme-shared-client"),
                ],
            ),
            (
                "prod",
                &[("tier_class", "prod"), ("client_id", "acme-shared-client")],
            ),
        ],
    )];
    let r = evaluate(&tier_input("tier_class"), &units, NOW).unwrap();
    assert_eq!(r.unjustified.len(), 1);
    assert_eq!(r.unjustified[0].key, "client_id");
}

// Go: TestEvaluateErrorsOnUndeclaredEnv.
#[test]
fn errors_on_undeclared_env() {
    let cfg = EnvUniquenessConfig {
        protected_tier: "prod".into(),
        environments: vec![env("prod", "prod")],
        ..Default::default()
    };
    let units = vec![unit(
        "svc/api-a",
        &[
            ("dev", &[("client_id", "acme-shared-client")]),
            ("prod", &[("client_id", "acme-shared-client")]),
        ],
    )];
    let err = evaluate(&cfg, &units, NOW).unwrap_err();
    assert_eq!(
        err,
        "env_uniqueness: environment \"dev\" found in data but not declared in any environment {} block"
    );
}

// Go: TestEvaluateStripsTierInputLeafBeforeDetection.
#[test]
fn strips_tier_input_leaf_before_detection() {
    let cfg = tier_input("tier_id");
    let units = vec![unit(
        "svc/api-a",
        &[
            (
                "dev",
                &[
                    ("tier_id", "nonprod"),
                    ("account_id", "acme-shared-account"),
                ],
            ),
            ("staging", &[("tier_id", "nonprod")]),
            (
                "prod",
                &[("tier_id", "prod"), ("account_id", "acme-shared-account")],
            ),
        ],
    )];
    let r = evaluate(&cfg, &units, NOW).unwrap();
    assert!(
        r.unjustified
            .iter()
            .chain(&r.report_only)
            .all(|v| v.key != "tier_id")
    );
    assert!(r.unjustified.iter().any(|v| v.key == "account_id"));
    for e in ["dev", "staging", "prod"] {
        assert!(
            units[0].inputs[e].contains_key("tier_id"),
            "caller's {e} inputs untouched"
        );
    }
}

// Go: TestEvaluateErrorsOnInvalidExtraKeyPattern.
#[test]
fn errors_on_invalid_extra_key_pattern() {
    let cfg = EnvUniquenessConfig {
        protected_tier: "prod".into(),
        environments: vec![env("prod", "prod")],
        extra_key_patterns: vec!["(unterminated[".into()],
        ..Default::default()
    };
    let units = vec![unit(
        "svc/api-a",
        &[("prod", &[("client_id", "acme-client")])],
    )];
    let err = evaluate(&cfg, &units, NOW).unwrap_err();
    assert!(
        err.starts_with("env_uniqueness: invalid extra_key_patterns entry \"(unterminated[\": "),
        "{err}"
    );
}

// Go: TestEvaluateErrorsOnInconsistentTierInput.
#[test]
fn errors_on_inconsistent_tier_input() {
    let units = vec![
        unit("svc/api-a", &[("dev", &[("tier_class", "nonprod")])]),
        unit("svc/api-b", &[("dev", &[("tier_class", "prod")])]),
    ];
    let err = evaluate(&tier_input("tier_class"), &units, NOW).unwrap_err();
    assert_eq!(
        err,
        "env_uniqueness: inconsistent tier for env \"dev\": \"nonprod\" vs \"prod\" (source.tier_input=\"tier_class\")"
    );
}

#[test]
fn errors_on_unresolvable_tier() {
    let units = vec![unit("svc/api-a", &[("dev", &[("x", "y")])])];
    let err = evaluate(&tier_input("tier_class"), &units, NOW).unwrap_err();
    assert_eq!(
        err,
        "env_uniqueness: no resolvable tier for env \"dev\" (source.tier_input=\"tier_class\")"
    );
}
