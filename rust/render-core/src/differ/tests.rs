use std::collections::BTreeMap;

use super::*;

fn inp(attr: &str) -> Input {
    Input {
        resource_type: String::new(),
        attr: attr.to_string(),
        before: Value::Null,
        after: Value::Null,
        sensitive: false,
        before_sensitive: Value::Null,
        after_sensitive: Value::Null,
        unknown: false,
        force_differ: String::new(),
        max_lines: 0,
        no_detect: false,
        sensitivity_only: false,
    }
}

fn s(v: &str) -> Value {
    Value::Str(v.to_string())
}

fn map(pairs: &[(&str, &str)]) -> Value {
    Value::Map(pairs.iter().map(|(k, v)| (k.to_string(), s(v))).collect())
}

fn levels(f: &Field) -> Vec<Level> {
    f.variants.iter().map(|v| v.level).collect()
}

fn twelve_keys(prefix: &str, val: &str) -> String {
    let mut out = prefix.to_string();
    for i in 0..12 {
        out.push_str(&format!("  k{i:02}: {val}\n"));
    }
    out
}

#[test]
fn scalar_leaf() {
    let f = diff(&Input {
        before: s("roles/viewer"),
        after: s("roles/editor"),
        ..inp("role")
    });
    assert!(!f.is_block());
    assert_eq!(f.leaves.len(), 1);
    let l = &f.leaves[0];
    assert_eq!(l.op, LeafOp::Change);
    assert_eq!(l.path, "role");
    assert_eq!(l.value(), r#""roles/viewer" → "roles/editor""#);
}

#[test]
fn sensitive_leaf() {
    let f = diff(&Input {
        before: s("a"),
        after: s("b"),
        sensitive: true,
        ..inp("password")
    });
    assert_eq!(f.leaves.len(), 1);
    assert_eq!(f.leaves[0].inline, "(sensitive value)");
}

#[test]
fn structural_redacts_nested_sensitive_leaf() {
    let sens = Value::Map(BTreeMap::from([(
        "password".to_string(),
        Value::Bool(true),
    )]));
    let f = diff(&Input {
        before: map(&[("cpu", "334m"), ("password", "hunter2")]),
        after: map(&[("cpu", "300m"), ("password", "hunter2")]),
        before_sensitive: sens.clone(),
        after_sensitive: sens,
        ..inp("spec")
    });
    assert!(f.is_block());
    let content = &f.variants[0].content;
    assert!(
        content.contains("334m") && content.contains("300m"),
        "{content}"
    );
    assert!(!content.contains("hunter2"), "{content}");
    assert!(content.contains("(sensitive value)"), "{content}");
}

#[test]
fn unknown_leaf() {
    let f = diff(&Input {
        unknown: true,
        ..inp("id")
    });
    assert_eq!(f.leaves.len(), 1);
    assert_eq!(f.leaves[0].inline, "(known after apply)");
}

#[test]
fn create_leaf() {
    let f = diff(&Input {
        after: s("app-api"),
        ..inp("account_id")
    });
    assert_eq!(f.leaves.len(), 1);
    assert_eq!(f.leaves[0].op, LeafOp::Add);
    assert_eq!(f.leaves[0].value(), r#""app-api""#);
}

#[test]
fn delete_leaf() {
    let f = diff(&Input {
        before: s("legacy"),
        ..inp("name")
    });
    assert_eq!(f.leaves.len(), 1);
    assert_eq!(f.leaves[0].op, LeafOp::Remove);
    assert_eq!(f.leaves[0].value(), r#""legacy""#);
}

#[test]
fn structured_yaml_ladder() {
    let f = diff(&Input {
        before: s(&twelve_keys("spec:\n", "old")),
        after: s(&twelve_keys("spec:\n", "new")),
        ..inp("manifest")
    });
    assert_eq!(
        levels(&f),
        vec![Level::Structural, Level::Summary, Level::Hidden]
    );
}

#[test]
fn forced_differ_line() {
    let f = diff(&Input {
        before: s("a: 1\n"),
        after: s("a: 2\n"),
        force_differ: "line".to_string(),
        ..inp("manifest")
    });
    assert_eq!(levels(&f)[0], Level::LineDiff);
}

#[test]
fn max_attribute_lines_ceiling() {
    let f = diff(&Input {
        before: s(&twelve_keys("spec:\n", "old")),
        after: s(&twelve_keys("spec:\n", "new")),
        max_lines: 1,
        ..inp("manifest")
    });
    assert_eq!(levels(&f)[0], Level::Summary);
}

#[test]
fn native_map_structural_summary_has_counts() {
    let before = Value::Map(
        (0..12)
            .map(|i| (format!("k{i:02}"), s(&format!("old{i}"))))
            .collect(),
    );
    let after = Value::Map(
        (0..12)
            .map(|i| (format!("k{i:02}"), s(&format!("new{i}"))))
            .collect(),
    );
    let f = diff(&Input {
        before,
        after,
        ..inp("settings")
    });
    assert_eq!(f.variants.len(), 3);
    let sum = &f.variants[1];
    assert_eq!(sum.level, Level::Summary);
    assert!(
        !sum.content.contains("0 lines") && !sum.content.contains("0 changed"),
        "{}",
        sum.content
    );
}

#[test]
fn base64_ladder() {
    let f = diff(&Input {
        before: s(&"QUJD".repeat(60)),
        after: s(&"WFla".repeat(60)),
        ..inp("data")
    });
    assert_eq!(levels(&f), vec![Level::Summary, Level::Hidden]);
    assert!(f.variants[0].content.contains("base64"));
}

#[test]
fn no_detect_forces_line() {
    let f = diff(&Input {
        before: s("spec:\n  replicas: 3\n  image: v1\n"),
        after: s("spec:\n  replicas: 5\n  image: v1\n"),
        no_detect: true,
        ..inp("manifest")
    });
    assert_eq!(levels(&f)[0], Level::LineDiff);
}

#[test]
fn forced_summary() {
    let f = diff(&Input {
        before: s("a\nb\nc\n"),
        after: s("a\nB\nc\n"),
        force_differ: "summary".to_string(),
        ..inp("x")
    });
    assert_eq!(f.variants[0].level, Level::Summary);
}

#[test]
fn structural_is_context_diff_block() {
    let f = diff(&Input {
        before: map(&[("env", "nonprod"), ("team", "old")]),
        after: map(&[("env", "nonprod"), ("team", "new")]),
        ..inp("labels")
    });
    assert!(f.is_block());
    assert_eq!(f.kind, "yaml");
    let rich = &f.variants[0].content;
    assert!(
        rich.contains("-team: old") && rich.contains("+team: new"),
        "{rich}"
    );
    assert!(rich.contains("env: nonprod"), "{rich}");
}

#[test]
fn structural_kind_json() {
    let f = diff(&Input {
        before: s(r#"{"a":1}"#),
        after: s(r#"{"a":2}"#),
        ..inp("policy")
    });
    assert_eq!(f.kind, "json");
    assert!(f.is_block());
}

#[test]
fn structural_create_all_add() {
    let f = diff(&Input {
        after: map(&[("team", "platform"), ("env", "prod")]),
        ..inp("labels")
    });
    assert!(f.is_block());
    let rich = &f.variants[0].content;
    assert!(!rich.contains("null"), "{rich}");
    assert!(
        rich.contains("+env: prod") && rich.contains("+team: platform"),
        "{rich}"
    );
}

#[test]
fn structural_ladder() {
    let before = Value::Map((0..12).map(|i| (format!("k{i:02}"), s("old"))).collect());
    let after = Value::Map((0..12).map(|i| (format!("k{i:02}"), s("new"))).collect());
    let f = diff(&Input {
        before,
        after,
        ..inp("settings")
    });
    assert_eq!(
        levels(&f),
        vec![Level::Structural, Level::Summary, Level::Hidden]
    );
}

#[test]
fn json_sniffing_on_create() {
    let f = diff(&Input {
        after: s(r#"{"a": 1, "b": 2}"#),
        ..inp("policy")
    });
    assert_eq!(f.kind, "json");
    assert!(f.is_block());
}

// builders_test.go

#[test]
fn detect_type() {
    assert!(detect(r#"{"a":1}"#) == ValueType::Json);
    assert!(detect("a: 1\nb: 2\n") == ValueType::Yaml);
    assert!(detect("just some text") == ValueType::Plain);
    assert!(detect("1: one\ntrue: yes\n") == ValueType::Plain);
    assert!(detect(&"QUJD".repeat(60)) == ValueType::Base64);
}

#[test]
fn context_diff_keeps_two_lines() {
    let out = context_diff("a: 1\nb: 2\nc: 3\nd: 4\n", "a: 1\nb: 2\nc: 9\nd: 4\n");
    assert!(out.contains("-c: 3") && out.contains("+c: 9"), "{out}");
    assert!(out.contains(" b: 2") && out.contains(" d: 4"), "{out}");
}

#[test]
fn context_diff_multi_hunk_separator() {
    let before = format!("a: 0\n{}z: 0\n", "x: 1\n".repeat(10));
    let after = format!("a: 9\n{}z: 9\n", "x: 1\n".repeat(10));
    assert!(context_diff(&before, &after).contains('⋮'));
}

#[test]
fn line_diff_marks_changes() {
    let out = line_diff("line1\nline2\n", "line1\nCHANGED\n");
    assert!(out.contains("-line2") && out.contains("+CHANGED"), "{out}");
}

#[test]
fn summary_line_counts() {
    let out = summary_line("content", "yaml", 412, 18);
    assert!(
        out.contains("412 lines") && out.contains("18 changed"),
        "{out}"
    );
}

// Go quirks the port reproduces.

#[test]
fn canonical_yaml_keeps_difflib_trailing_context_line() {
    // SplitLines adds a "\n" line after the final newline, which shows as a " " context line.
    let f = diff(&Input {
        before: map(&[("env", "nonprod")]),
        after: map(&[("env", "nonprod"), ("team", "platform")]),
        ..inp("labels")
    });
    assert_eq!(f.variants[0].content, " env: nonprod\n+team: platform\n ");
}

#[test]
fn float_scalars_print_like_go() {
    let f = diff(&Input {
        before: Value::Float(3.0),
        after: Value::Float(1e21),
        ..inp("n")
    });
    assert_eq!(f.leaves[0].value(), "3 → 1e+21");
}
