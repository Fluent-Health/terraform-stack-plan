use std::collections::{BTreeMap, BTreeSet};

use super::*;

fn default_classifier() -> Classifier {
    Classifier {
        val_pats: default_value_patterns(),
        key_pats: default_key_patterns(),
        qty_suffixes: default_quantity_suffixes(),
        ..Default::default()
    }
}

fn tokens(entries: &[(&str, &[&str])]) -> BTreeMap<String, Vec<String>> {
    entries
        .iter()
        .map(|(e, t)| (e.to_string(), t.iter().map(|s| s.to_string()).collect()))
        .collect()
}

// Go: TestIsIdentifierQuantitySuffix.
#[test]
fn quantity_suffix_is_never_identifier() {
    assert!(!default_classifier().is_identifier("jwt_expiration_ms", &Value::Int(86_400_000)));
}

// Go: TestIsIdentifierKeyPattern.
#[test]
fn key_pattern_makes_identifier() {
    assert!(default_classifier().is_identifier("system_user_id", &Value::str("615840001234")));
}

// Go: TestIsIdentifierEmptyAndBoolAreNever.
#[test]
fn empty_and_bool_are_never_identifiers() {
    let c = default_classifier();
    assert!(!c.is_identifier("client_id", &Value::str("")));
    assert!(!c.is_identifier("client_id", &Value::Bool(false)));
}

// Go: TestIsIdentifierListAnyElement.
#[test]
fn list_is_identifier_if_any_element_is() {
    assert!(
        default_classifier()
            .is_identifier("allowed_hosts", &Value::list(&["acme-dev.example.com"]))
    );
}

// Go: TestIsIdentifierEnvTokenFolded.
#[test]
fn env_token_folded_into_value_matching() {
    let mut c = default_classifier();
    c.env_tokens = tokens(&[("dev", &["acme-dev"])]);
    assert!(c.is_identifier("some_setting", &Value::str("acme-dev")));
    assert!(!c.is_identifier("some_setting", &Value::str("unrelated-value")));
}

// Go: TestIsEnvScoped.
#[test]
fn env_scoped_by_whole_segment() {
    let c = Classifier {
        scoped_segs: BTreeSet::from(["dev".to_string()]),
        ..Default::default()
    };
    assert!(c.is_env_scoped("environments.dev.project"));
    assert!(!c.is_env_scoped("enable_dev_mode"));
}

// Go: TestDeriveTokensAndSegs.
#[test]
fn derive_tokens_from_template() {
    let (tokens, segs) =
        derive_tokens_and_segs(&["dev".into(), "prod".into()], "acme-{env}", &[], &[]);
    assert!(tokens["dev"].contains(&"acme-dev".to_string()));
    assert!(segs.contains("dev"));
    assert!(segs.contains("acme-dev"));
}

// Go: TestBoundaryTokenPattern.
#[test]
fn boundary_token_matches_whole_tokens_only() {
    let p = boundary_token_pattern("acme-dev");
    assert!(p.is_match("acme-dev.example.com"));
    assert!(!p.is_match("acme-development"));
}

// Go: TestNewClassifierPrecompilesTokenPatterns.
#[test]
fn new_classifier_precompiles_token_patterns() {
    let c = Classifier::new(
        default_value_patterns(),
        default_key_patterns(),
        default_quantity_suffixes(),
        tokens(&[("dev", &["acme-dev"])]),
        BTreeSet::new(),
    );
    assert!(c.token_pats.is_some());
    assert!(c.is_identifier("some_setting", &Value::str("acme-dev")));
    assert!(!c.is_identifier("some_setting", &Value::str("unrelated-value")));
}

// Go: TestClassifierStructLiteralTokenFoldingUncached.
#[test]
fn struct_literal_classifier_compiles_tokens_on_the_fly() {
    let mut c = default_classifier();
    c.env_tokens = tokens(&[("dev", &["acme-dev"])]);
    assert!(c.token_pats.is_none());
    assert!(c.is_identifier("some_setting", &Value::str("acme-dev")));
    assert!(!c.is_identifier("some_setting", &Value::str("unrelated-value")));
}

// Go: TestIsIdentifierIntegralFloat64LongNumeric.
#[test]
fn huge_integral_float_still_looks_numeric() {
    assert!(
        default_classifier()
            .is_identifier("blob", &Value::Float(1_234_567_890_123_456_789_012_345.0))
    );
}

// Go: TestIsIdentifierNonIntegralFloat64NotFlagged.
#[test]
fn non_integral_float_not_flagged() {
    assert!(!default_classifier().is_identifier("blob", &Value::Float(1.5)));
}

// Go: TestDeriveTokensAndSegsExtras.
#[test]
fn derive_tokens_with_extras() {
    let (tokens, segs) = derive_tokens_and_segs(
        &["dev".into()],
        "",
        &[("dev".into(), vec!["acme-dev-svc".into()])],
        &["shared-infra".into()],
    );
    assert_eq!(tokens["dev"], vec!["dev", "acme-dev-svc"]);
    assert!(segs.contains("acme-dev-svc"));
    assert!(segs.contains("shared-infra"));
}

#[test]
fn long_numeric_pattern_is_ascii_only() {
    assert!(!default_classifier().is_identifier("blob", &Value::str("١٢٣٤٥٦٧")));
    assert!(default_classifier().is_identifier("blob", &Value::str("1234567")));
}
