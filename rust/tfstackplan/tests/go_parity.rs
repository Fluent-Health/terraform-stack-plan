//! Property test: mutate the recorded plan fixtures at random, render them with the Go binary
//! and with the Rust front door, and require identical stdout, stderr, exit code and
//! classification JSON.
//!
//! Skipped (with a note) when the Go binary is absent: `TFSTACKPLAN_GO_BIN`, else
//! `rust/.go/tfstackplan-go`. `PROPTEST_CASES` sets the number of cases (default 96).

#![expect(
    clippy::unwrap_used,
    reason = "the helpers run only inside this test, where a panic is how it fails"
)]

use std::path::{Path, PathBuf};
use std::process::Command;

use proptest::prelude::*;
use serde_json::{Map, Value, json};

fn root() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("..")
}

fn go_bin() -> Option<PathBuf> {
    let p = std::env::var_os("TFSTACKPLAN_GO_BIN")
        .map(PathBuf::from)
        .unwrap_or_else(|| root().join(".go/tfstackplan-go"));
    p.is_file().then(|| std::fs::canonicalize(p).ok()).flatten()
}

/// Every fixture plan: (stack name, plan JSON).
fn fixtures() -> Vec<(String, Value)> {
    let mut out = Vec::new();
    let base = root().join("cases/fixtures");
    for set in ["small", "big", "state-ops", "links"] {
        let dir = base.join(set);
        let mut stack = Vec::new();
        collect(&dir, &dir, &mut stack);
        stack.sort_by(|a, b| a.0.cmp(&b.0));
        for (name, path) in stack {
            let text = std::fs::read_to_string(path).unwrap();
            out.push((
                format!("{set}/{name}"),
                serde_json::from_str(&text).unwrap(),
            ));
        }
    }
    out
}

fn collect(root: &Path, dir: &Path, out: &mut Vec<(String, PathBuf)>) {
    for e in std::fs::read_dir(dir).unwrap() {
        let p = e.unwrap().path();
        if p.is_dir() {
            collect(root, &p, out);
        } else if p.file_name().is_some_and(|n| n == "tfplan.json") {
            let rel = p.parent().unwrap().strip_prefix(root).unwrap();
            out.push((rel.to_string_lossy().into_owned(), p));
        }
    }
}

/// Strings that exercise detection, quoting and YAML/JSON round trips.
fn tricky_string() -> impl Strategy<Value = String> {
    prop_oneof![
        "[ -~]{0,12}",
        "\\PC{0,20}",
        Just("yes".to_string()),
        Just("2024-01-31".to_string()),
        Just("0777".to_string()),
        Just("~".to_string()),
        Just("a: 1\nb:\n  - x\n  - 'y'\n".to_string()),
        Just("key: value with: colon\nlist: [1, 2.5, true, null]\n".to_string()),
        Just("- one\n- two: 2\n  three: 0x1F\n".to_string()),
        Just("{\"a\": [1, 2, {\"b\": null}], \"c\": \"<tag>&\"}".to_string()),
        Just("[1e-7, 1e21, 123456789, 0.1]".to_string()),
        Just("line one\nline two\n\tindented\n".to_string()),
        "[A-Za-z0-9+/]{100,140}",
        "[a-z ]{60,200}",
        Just("x".repeat(70)),
        Just("base: &b\n  x: 1\n  y: [a, b]\nchild:\n  <<: *b\n  y: 2\n".to_string()),
        Just("when: 2001-12-14t21:59:43.10-05:00\nday: 2002-12-14\nnum: 0o17\nbig: 18446744073709551616\nneg: -0b101\nf: .inf\nn: ~\n".to_string()),
        Just("dup: 1\ndup: 2\n".to_string()),
        Just("? complex\n: value\n1: one\ntrue: yes\nnull: x\n".to_string()),
        Just("text: |\n  literal block\n  second line\nfolded: >\n  folded\n  text\n".to_string()),
        Just("---\na: 1\n---\nb: 2\n".to_string()),
        Just("emoji: \"\u{1F600} ok\"\nnbsp: \"a\u{a0}b\"\nctrl: \"\\x07\"\n".to_string()),
        Just("  leading: space\n".to_string()),
        Just("a:\r\n  - b\r\n".to_string()),
        Just("{\"html\": \"<b>&amp;</b>\", \"u\": \"\\u2028\\u00e9\", \"n\": [1.0, -0, 1E400]}".to_string()),
        Just("{\"a\": 1,}".to_string()),
        Just("[\"multi\\nline\", \" trailing \", \"yes\", \"1.5\", \"\"]".to_string()),
        Just("{\"z\": 1, \"a\": {\"y\": [], \"b\": null}, \"m\": \"x\"}".to_string()),
        (0u32..5000).prop_map(|n| (0..n % 40).map(|i| format!("row {i}: value {}\n", i * 7)).collect()),
    ]
}

fn leaf() -> impl Strategy<Value = Value> {
    prop_oneof![
        Just(Value::Null),
        any::<bool>().prop_map(Value::Bool),
        any::<i64>().prop_map(|n| json!(n)),
        (-1e25f64..1e25).prop_map(|f| json!(f)),
        prop_oneof![
            Just(1e-7),
            Just(1e21),
            Just(123456.7),
            Just(0.000012),
            Just(-0.0),
            Just(1e6)
        ]
        .prop_map(|f| json!(f)),
        tricky_string().prop_map(Value::String),
    ]
}

fn value() -> impl Strategy<Value = Value> {
    leaf().prop_recursive(3, 24, 5, |inner| {
        prop_oneof![
            prop::collection::vec(inner.clone(), 0..5).prop_map(Value::Array),
            prop::collection::btree_map("[a-z_]{1,8}", inner, 0..5)
                .prop_map(|m| Value::Object(m.into_iter().collect::<Map<_, _>>())),
        ]
    })
}

/// One edit to one resource change.
#[derive(Debug, Clone)]
enum Edit {
    SetAttr {
        rc: usize,
        after: bool,
        attr: String,
        v: Value,
    },
    Unknown {
        rc: usize,
        attr: String,
    },
    Sensitive {
        rc: usize,
        after: bool,
        attr: String,
        v: Value,
    },
    Actions {
        rc: usize,
        actions: Vec<&'static str>,
    },
    Drop {
        rc: usize,
    },
}

fn edit() -> impl Strategy<Value = Edit> {
    let attr = prop_oneof![
        Just("labels".to_string()),
        Just("manifest".to_string()),
        Just("project".to_string()),
        Just("name".to_string()),
        "[a-z_]{1,10}",
    ];
    let sens = prop_oneof![
        Just(json!(true)),
        Just(json!(false)),
        Just(json!({"a": true})),
        Just(json!([true, false])),
    ];
    let actions = prop_oneof![
        Just(vec!["create"]),
        Just(vec!["update"]),
        Just(vec!["delete"]),
        Just(vec!["delete", "create"]),
        Just(vec!["create", "delete"]),
        Just(vec!["no-op"]),
        Just(vec!["forget"]),
        Just(vec!["read"]),
    ];
    prop_oneof![
        6 => (any::<usize>(), any::<bool>(), attr.clone(), value())
            .prop_map(|(rc, after, attr, v)| Edit::SetAttr { rc, after, attr, v }),
        1 => (any::<usize>(), attr.clone()).prop_map(|(rc, attr)| Edit::Unknown { rc, attr }),
        1 => (any::<usize>(), any::<bool>(), attr, sens)
            .prop_map(|(rc, after, attr, v)| Edit::Sensitive { rc, after, attr, v }),
        1 => (any::<usize>(), actions).prop_map(|(rc, actions)| Edit::Actions { rc, actions }),
        1 => any::<usize>().prop_map(|rc| Edit::Drop { rc }),
    ]
}

fn obj_field<'a>(v: &'a mut Value, key: &str) -> &'a mut Map<String, Value> {
    let o = v.as_object_mut().unwrap();
    if !o.get(key).is_some_and(Value::is_object) {
        o.insert(key.to_string(), json!({}));
    }
    o.get_mut(key).unwrap().as_object_mut().unwrap()
}

fn apply(plans: &mut [(String, Value)], edits: &[Edit]) {
    // Flatten (stack, rc index) pairs so an edit can land anywhere.
    let slots: Vec<(usize, usize)> = plans
        .iter()
        .enumerate()
        .flat_map(|(si, (_, p))| {
            let n = p["resource_changes"].as_array().map_or(0, Vec::len);
            (0..n).map(move |ri| (si, ri))
        })
        .collect();
    if slots.is_empty() {
        return;
    }
    let mut dropped = Vec::new();
    for e in edits {
        let rc = match e {
            Edit::SetAttr { rc, .. }
            | Edit::Unknown { rc, .. }
            | Edit::Sensitive { rc, .. }
            | Edit::Actions { rc, .. }
            | Edit::Drop { rc } => *rc,
        };
        let (si, ri) = slots[rc % slots.len()];
        let change = plans[si].1["resource_changes"][ri]
            .as_object_mut()
            .unwrap()
            .entry("change")
            .or_insert_with(|| json!({}));
        if !change.is_object() {
            *change = json!({});
        }
        match e {
            Edit::SetAttr { after, attr, v, .. } => {
                let side = if *after { "after" } else { "before" };
                obj_field(change, side).insert(attr.clone(), v.clone());
            }
            Edit::Unknown { attr, .. } => {
                obj_field(change, "after_unknown").insert(attr.clone(), json!(true));
            }
            Edit::Sensitive { after, attr, v, .. } => {
                let side = if *after {
                    "after_sensitive"
                } else {
                    "before_sensitive"
                };
                obj_field(change, side).insert(attr.clone(), v.clone());
            }
            Edit::Actions { actions, .. } => {
                change["actions"] = json!(actions);
            }
            Edit::Drop { .. } => dropped.push((si, ri)),
        }
    }
    dropped.sort();
    dropped.dedup();
    for (si, ri) in dropped.into_iter().rev() {
        plans[si].1["resource_changes"]
            .as_array_mut()
            .unwrap()
            .remove(ri);
    }
}

/// Plans are written by hand so key order can be shuffled: serde_json's map is sorted, and Go
/// and the port must not depend on the order keys arrive in.
fn write_json(out: &mut String, v: &Value, reverse_keys: bool) {
    match v {
        Value::Object(m) => {
            let mut entries: Vec<(&String, &Value)> = m.iter().collect();
            if reverse_keys {
                entries.reverse();
            }
            out.push('{');
            for (i, (k, v)) in entries.into_iter().enumerate() {
                if i > 0 {
                    out.push(',');
                }
                out.push_str(&serde_json::to_string(k).unwrap());
                out.push(':');
                write_json(out, v, reverse_keys);
            }
            out.push('}');
        }
        Value::Array(items) => {
            out.push('[');
            for (i, v) in items.iter().enumerate() {
                if i > 0 {
                    out.push(',');
                }
                write_json(out, v, reverse_keys);
            }
            out.push(']');
        }
        _ => out.push_str(&serde_json::to_string(v).unwrap()),
    }
}

/// The links fixture's set index, and the source tree its stack names resolve against.
const LINKS_SET: usize = 3;
const LINKS_REPO_ROOT: &str = "rust/cases/fixtures/links-src";
const LINKS_CONFIG: &str = "internal/config/testdata/links.hcl";

#[derive(Debug, Clone)]
struct Run {
    set: usize,
    edits: Vec<Edit>,
    config: Option<&'static str>,
    max_bytes: &'static str,
    details: &'static str,
    reverse_keys: bool,
}

fn run_strategy() -> impl Strategy<Value = Run> {
    (
        0usize..4,
        prop::collection::vec(edit(), 0..12),
        prop_oneof![
            Just(None),
            Just(Some("internal/config/testdata/full.hcl")),
            Just(Some("internal/config/testdata/emit.hcl")),
            Just(Some("internal/config/testdata/derive.hcl")),
            Just(Some("examples/.tfstackplan.hcl")),
            Just(Some(LINKS_CONFIG)),
            Just(Some("rust/cases/fixtures/prod-like.hcl")),
        ],
        prop_oneof![
            Just("0"),
            Just("400"),
            Just("3000"),
            Just("9000"),
            Just("20000"),
            Just("60000")
        ],
        prop_oneof![Just("open"), Just("closed"), Just("auto")],
        any::<bool>(),
    )
        .prop_map(
            |(set, edits, config, max_bytes, details, reverse_keys)| Run {
                set,
                edits,
                config,
                max_bytes,
                details,
                reverse_keys,
            },
        )
}

struct Outcome {
    code: Option<i32>,
    stdout: String,
    stderr: String,
    classes: Option<String>,
}

fn invoke(bin: &Path, dir: &Path, r: &Run, out_name: &str, go: &Path) -> Outcome {
    let classes = dir.join(format!("{out_name}.json"));
    let mut cmd = Command::new(bin);
    cmd.current_dir(root().join(".."))
        .env("TFSTACKPLAN_GO_BIN", go)
        .env_remove("TFSTACKPLAN_IMPL")
        .env_remove("TFSTACKPLAN_IMPL_RENDER")
        .arg("render")
        .arg("--plans-dir")
        .arg(dir.join("plans"))
        .args(["--max-bytes", r.max_bytes, "--details", r.details])
        .arg("--emit-classification-json")
        .arg(&classes)
        // Every run resolves resource links against the links fixture's .tf tree; stacks
        // of the other sets have no sources there and fall back to the stack link.
        .args(["--repo-root", LINKS_REPO_ROOT])
        .args(["--link-var", "sha=0123456789abcdef", "--link-var", "pr=42"]);
    // The links set always renders with resource links on.
    let config = match r.config {
        None if r.set == LINKS_SET => Some(LINKS_CONFIG),
        c => c,
    };
    if let Some(c) = config {
        cmd.args(["--config", c]);
    }
    let out = cmd.output().unwrap();
    let classes_text = std::fs::read_to_string(&classes).ok();
    let _ = std::fs::remove_file(&classes);
    Outcome {
        code: out.status.code(),
        stdout: String::from_utf8_lossy(&out.stdout).into_owned(),
        stderr: String::from_utf8_lossy(&out.stderr).into_owned(),
        classes: classes_text,
    }
}

/// The first differing line of two outputs, with a little context.
fn first_diff(rust: &str, go: &str) -> String {
    let (r, g): (Vec<&str>, Vec<&str>) = (rust.lines().collect(), go.lines().collect());
    let i = r.iter().zip(&g).take_while(|(a, b)| a == b).count();
    let from = i.saturating_sub(3);
    format!(
        "line {}\n--- rust\n{}\n--- go\n{}",
        i + 1,
        r[from..(i + 3).min(r.len())].join("\n"),
        g[from..(i + 3).min(g.len())].join("\n")
    )
}

#[test]
fn rust_render_matches_go_on_mutated_plans() {
    let Some(go) = go_bin() else {
        let msg = "Go binary not found (set TFSTACKPLAN_GO_BIN or build rust/.go/tfstackplan-go)";
        assert!(
            std::env::var("CI").map_or(true, |v| v.is_empty() || v == "false"),
            "{msg}: the Go parity test cannot run, and CI must not skip it"
        );
        eprintln!(
            "\n!!!!!!!!\n!!! SKIPPED: Go parity property test did not run. {msg}\n!!!!!!!!\n"
        );
        return;
    };
    let rust = PathBuf::from(env!("CARGO_BIN_EXE_tfstackplan"));
    let all = fixtures();
    let cases = std::env::var("PROPTEST_CASES")
        .ok()
        .and_then(|v| v.parse().ok())
        .unwrap_or(96);
    let mut runner = proptest::test_runner::TestRunner::new(ProptestConfig {
        cases,
        failure_persistence: None,
        ..ProptestConfig::default()
    });
    runner
        .run(&run_strategy(), |r| {
            let set = ["small/", "big/", "state-ops/", "links/"][r.set];
            let mut plans: Vec<(String, Value)> = all
                .iter()
                .filter(|(n, _)| n.starts_with(set))
                .map(|(n, v)| (n[set.len()..].to_string(), v.clone()))
                .collect();
            apply(&mut plans, &r.edits);
            let dir = tempfile::Builder::new()
                .prefix("tfsp-parity-")
                .tempdir()
                .unwrap();
            for (name, plan) in &plans {
                let d = dir.path().join("plans").join(name);
                std::fs::create_dir_all(&d).unwrap();
                let mut text = String::new();
                write_json(&mut text, plan, r.reverse_keys);
                std::fs::write(d.join("tfplan.json"), text).unwrap();
            }
            let g = invoke(&go, dir.path(), &r, "go", &go);
            let s = invoke(&rust, dir.path(), &r, "rust", &go);
            prop_assert!(
                s.stdout == g.stdout,
                "stdout differs:\n{}",
                first_diff(&s.stdout, &g.stdout)
            );
            prop_assert!(
                s.stderr == g.stderr,
                "stderr differs:\n{}",
                first_diff(&s.stderr, &g.stderr)
            );
            prop_assert_eq!(s.code, g.code, "exit code differs");
            prop_assert_eq!(&s.classes, &g.classes, "classification JSON differs");
            Ok(())
        })
        .unwrap();
}
