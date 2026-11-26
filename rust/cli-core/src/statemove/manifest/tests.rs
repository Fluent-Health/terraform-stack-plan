use super::*;
use crate::statemove::Move;

fn shim(stack: &str, key: &str, ops: Vec<Op>) -> FoundShim {
    FoundShim {
        stack: stack.into(),
        key: key.into(),
        ops,
    }
}

fn xm(dest: &str, key: &str, source: &str, pairs: &[(&str, &str)]) -> FoundXMove {
    FoundXMove {
        path: String::new(),
        dest_stack: dest.into(),
        key: key.into(),
        xmove: XMove {
            source_stack: source.into(),
            pairs: pairs.iter().map(|(f, t)| Move::new(f, t)).collect(),
        },
    }
}

fn no_dirs(_: &str) -> bool {
    false
}

// Go: TestMovesManifestTwoSided.
#[test]
fn two_sided() {
    let shims = vec![
        shim(
            "service-projects/svc-a",
            "PR-7",
            vec![Op::removed("google_project_iam_member.node")],
        ),
        shim(
            "cluster/svc-a",
            "PR-7",
            vec![Op::import(
                "google_project_iam_member.node",
                "proj roles/x sa",
            )],
        ),
    ];
    let got = collect_state_moves(&shims, &[], "", &no_dirs);
    assert_eq!(
        got["service-projects/svc-a"],
        vec!["google_project_iam_member.node"]
    );
    assert_eq!(got["cluster/svc-a"], vec!["google_project_iam_member.node"]);
}

// Go: TestMovesManifestXMoveBothStacks.
#[test]
fn xmove_both_stacks() {
    let x = vec![xm(
        "dst",
        "PR-9",
        "src",
        &[("module.a.google_x.y", "module.b.google_x.y")],
    )];
    let got = collect_state_moves(&[], &x, "", &|p| p == "src");
    assert_eq!(got["src"], vec!["module.a.google_x.y"]);
    assert_eq!(got["dst"], vec!["module.b.google_x.y"]);
}

// Go: TestMovesManifestXMoveRelativeSource.
#[test]
fn xmove_relative_source() {
    let x = vec![xm(
        "stacks/tier/workloads/dst",
        "PR-42",
        "src",
        &[("module.x.google_y.z", "module.x.google_y.z")],
    )];
    let exists = |p: &str| p == "stacks/tier/src" || p == "stacks/tier/workloads/dst";
    let got = collect_state_moves(&[], &x, "", &exists);
    assert_eq!(got["stacks/tier/src"], vec!["module.x.google_y.z"]);
    assert_eq!(
        got["stacks/tier/workloads/dst"],
        vec!["module.x.google_y.z"]
    );
}

// Go: TestMovesManifestPRFilter.
#[test]
fn pr_filter() {
    let shims = vec![
        shim("s", "PR-1", vec![Op::removed("a.b")]),
        shim("s", "PR-2", vec![Op::removed("c.d")]),
    ];
    let got = collect_state_moves(&shims, &[], "PR-1", &no_dirs);
    assert_eq!(got["s"], vec!["a.b"]);
}

#[test]
fn json_matches_go_marshal_indent() {
    let mut m = BTreeMap::new();
    assert_eq!(manifest_json(&m), "{}\n");
    m.insert("b".to_string(), vec!["x".to_string(), "y".to_string()]);
    m.insert("a".to_string(), vec!["z".to_string()]);
    assert_eq!(
        manifest_json(&m),
        "{\n  \"a\": [\n    \"z\"\n  ],\n  \"b\": [\n    \"x\",\n    \"y\"\n  ]\n}\n"
    );
}
