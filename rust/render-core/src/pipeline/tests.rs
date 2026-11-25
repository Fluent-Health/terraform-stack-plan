use super::*;

#[test]
fn base_vars_derives_sha_short_and_skips_bad_pairs() {
    let v = base_vars(&[
        "sha=0123456789abcdef".to_string(),
        "=x".to_string(),
        "pr=42".to_string(),
        "nokey".to_string(),
    ]);
    assert_eq!(v.get("sha_short").map(String::as_str), Some("0123456"));
    assert_eq!(v.get("pr").map(String::as_str), Some("42"));
    assert_eq!(v.len(), 3);
}

#[test]
fn bad_details_is_an_error_after_all_stacks_parse() {
    let cfg = Config::default();
    let moves = Manifest::default();
    let mut b = Builder::new(&cfg, &moves, "T", "m", &[]);
    b.add_stack(
        "a",
        br#"{"format_version":"1.2","resource_changes":[]}"#,
        "a",
        None,
    )
    .unwrap();
    let err = b.finish("sideways", 0).unwrap_err();
    assert_eq!(err, r#"--details must be auto|open|closed, got "sideways""#);
}

#[test]
fn sidecar_lists_stacks_sorted_with_null_icons() {
    let cfg = crate::config::load(
        "p.hcl",
        "classification {\n  default = \"safe\"\n  preset \"iam\" {}\n}\n",
    )
    .unwrap();
    let moves = Manifest::default();
    let mut b = Builder::new(&cfg, &moves, "T", "m", &[]);
    for name in ["b", "a"] {
        b.add_stack(name, br#"{"format_version":"1.2"}"#, name, None)
            .unwrap();
    }
    let out = b.finish("closed", 0).unwrap();
    assert_eq!(
        out.classification_json.unwrap(),
        "{\n  \"stacks\": {\n    \"a\": {\n      \"categories\": [],\n      \"counts\": {}\n    },\n    \"b\": {\n      \"categories\": [],\n      \"counts\": {}\n    }\n  },\n  \"summary\": {\n    \"categories\": []\n  }\n}"
    );
}
