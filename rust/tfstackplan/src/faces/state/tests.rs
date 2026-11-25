use std::collections::BTreeMap;
use std::path::{Path, PathBuf};

use tfstackplan_cli::statemove::{
    Move, Op, XMove, render_shim, render_xmove, shim_file_name, xmove_file_name,
};

use super::*;

fn args(a: &[&str]) -> Vec<String> {
    a.iter().map(|s| s.to_string()).collect()
}

fn run_state(a: &[&str]) -> ExitCode {
    run(&args(a))
}

fn ok(c: ExitCode) -> bool {
    c == ExitCode::SUCCESS
}

fn tmp() -> tempfile::TempDir {
    tempfile::Builder::new()
        .prefix("tfsp-state-")
        .tempdir()
        .unwrap()
}

fn s(p: &Path) -> String {
    p.to_string_lossy().into_owned()
}

fn mkdir(p: &Path) {
    std::fs::create_dir_all(p).unwrap();
}

/// Go's `writeStackPlan`: a tfplan.json from (address, type, action) rows.
fn write_stack_plan(stack: &str, rows: &[(&str, &str, &str)]) -> tempfile::TempDir {
    let root = tmp();
    let dir = root.path().join(stack);
    mkdir(&dir);
    let rc: Vec<String> = rows
        .iter()
        .map(|(a, t, act)| {
            format!(r#"{{"address":"{a}","type":"{t}","change":{{"actions":["{act}"]}}}}"#)
        })
        .collect();
    std::fs::write(
        dir.join("tfplan.json"),
        format!(
            r#"{{"format_version":"1.2","resource_changes":[{}]}}"#,
            rc.join(",")
        ),
    )
    .unwrap();
    root
}

fn shim_path(root: &Path, stack: &str, key: &str) -> PathBuf {
    root.join(stack).join(shim_file_name(key))
}

// Go: TestStateMoveGeneratesShim.
#[test]
fn state_move_generates_shim() {
    let root = write_stack_plan(
        "stacks/a",
        &[
            ("aws_s3_bucket.old", "aws_s3_bucket", "delete"),
            ("aws_s3_bucket.new", "aws_s3_bucket", "create"),
        ],
    );
    assert!(ok(run_state(&[
        "move",
        "--dir",
        &s(root.path()),
        "--stack",
        "stacks/a",
        "--pr",
        "123",
        "aws_s3_bucket.old",
        "aws_s3_bucket.new",
    ])));
    let data = std::fs::read_to_string(shim_path(root.path(), "stacks/a", "PR-123")).unwrap();
    assert!(data.contains("from = aws_s3_bucket.old"), "{data}");
}

// Go: TestStateMoveFailsClosed.
#[test]
fn state_move_fails_closed() {
    let root = write_stack_plan(
        "stacks/a",
        &[("aws_s3_bucket.old", "aws_s3_bucket", "delete")],
    );
    assert!(!ok(run_state(&[
        "move",
        "--dir",
        &s(root.path()),
        "--stack",
        "stacks/a",
        "--pr",
        "123",
        "aws_s3_bucket.old",
        "aws_s3_bucket.new",
    ])));
    assert!(!shim_path(root.path(), "stacks/a", "PR-123").exists());
}

/// Go's `writeTwoStackPlans`.
fn write_two_stack_plans(
    src: (&str, &str, &str, &str),
    dst: (&str, &str, &str),
) -> tempfile::TempDir {
    let root = tmp();
    let mk = |stack: &str, body: String| {
        let dir = root.path().join(stack);
        mkdir(&dir);
        std::fs::write(dir.join("tfplan.json"), body).unwrap();
    };
    mk(
        src.0,
        format!(
            r#"{{"format_version":"1.2","resource_changes":[{{"address":"{}","type":"{}","change":{{"actions":["delete"],"before":{{"id":"{}"}}}}}}]}}"#,
            src.1, src.2, src.3
        ),
    );
    mk(
        dst.0,
        format!(
            r#"{{"format_version":"1.2","resource_changes":[{{"address":"{}","type":"{}","change":{{"actions":["create"]}}}}]}}"#,
            dst.1, dst.2
        ),
    );
    root
}

// Go: TestStateMoveCrossStack.
#[test]
fn state_move_cross_stack() {
    let root = write_two_stack_plans(
        ("stacks/a", "aws_s3_bucket.x", "aws_s3_bucket", "my-bucket"),
        ("stacks/b", "aws_s3_bucket.x", "aws_s3_bucket"),
    );
    assert!(ok(run_state(&[
        "move",
        "--dir",
        &s(root.path()),
        "--pr",
        "5",
        "stacks/a:aws_s3_bucket.x",
        "stacks/b:aws_s3_bucket.x",
    ])));
    let src = std::fs::read_to_string(shim_path(root.path(), "stacks/a", "PR-5")).unwrap();
    assert!(
        src.contains("removed {") && src.contains("destroy = false"),
        "{src}"
    );
    let dst = std::fs::read_to_string(shim_path(root.path(), "stacks/b", "PR-5")).unwrap();
    assert!(
        dst.contains("import {") && dst.contains("id = \"my-bucket\""),
        "{dst}"
    );
}

// Go: TestStateMoveCrossStackFailsClosed.
#[test]
fn state_move_cross_stack_fails_closed() {
    let root = write_two_stack_plans(
        ("stacks/a", "aws_s3_bucket.x", "aws_s3_bucket", "id"),
        ("stacks/b", "aws_s3_bucket.x", "aws_s3_bucket"),
    );
    std::fs::write(
        root.path().join("stacks/b/tfplan.json"),
        r#"{"format_version":"1.2","resource_changes":[]}"#,
    )
    .unwrap();
    assert!(!ok(run_state(&[
        "move",
        "--dir",
        &s(root.path()),
        "--pr",
        "5",
        "stacks/a:aws_s3_bucket.x",
        "stacks/b:aws_s3_bucket.x",
    ])));
    assert!(!shim_path(root.path(), "stacks/a", "PR-5").exists());
}

// Go: TestStateListAndCleanup.
#[test]
fn state_list_and_cleanup() {
    let root = write_stack_plan(
        "stacks/a",
        &[
            ("aws_s3_bucket.old", "aws_s3_bucket", "delete"),
            ("aws_s3_bucket.new", "aws_s3_bucket", "create"),
        ],
    );
    let r = s(root.path());
    assert!(ok(run_state(&[
        "move",
        "--dir",
        &r,
        "--stack",
        "stacks/a",
        "--pr",
        "7",
        "aws_s3_bucket.old",
        "aws_s3_bucket.new"
    ])));
    assert!(ok(run_state(&["list", "--dir", &r])));
    assert!(ok(run_state(&["cleanup", "--dir", &r, "--all"])));
    assert!(!shim_path(root.path(), "stacks/a", "PR-7").exists());
}

// Go: TestStateCleanupApplied.
#[test]
fn state_cleanup_applied() {
    let root = tmp();
    let shim_dir = root.path().join("stacks/a");
    mkdir(&shim_dir);
    let shim_file = shim_dir.join(shim_file_name("PR-5"));
    std::fs::write(&shim_file, "# tfstackplan:key=PR-5\n").unwrap();
    for dst in ["stacks/b", "stacks/c"] {
        let d = root.path().join(dst);
        mkdir(&d);
        let xm = XMove {
            source_stack: "stacks/a".into(),
            pairs: vec![Move::new("r.x", "r.x")],
        };
        std::fs::write(d.join(xmove_file_name("PR-5")), render_xmove("PR-5", &xm)).unwrap();
    }
    assert!(ok(run_state(&[
        "cleanup",
        "--dir",
        &s(root.path()),
        "--applied"
    ])));
    assert!(shim_file.exists());
    for dst in ["stacks/b", "stacks/c"] {
        assert!(!root.path().join(dst).join(xmove_file_name("PR-5")).exists());
    }
}

/// Go's `writeSrcPlanWithPriorState`.
fn write_src_plan_with_prior_state(root: &Path, stack: &str, addr: &str, typ: &str) {
    let dir = root.join(stack);
    mkdir(&dir);
    std::fs::write(
        dir.join("tfplan.json"),
        format!(
            r#"{{"format_version":"1.2","prior_state":{{"format_version":"0.1","values":{{"root_module":{{"resources":[{{"address":"{addr}","type":"{typ}","provider_name":"registry.terraform.io/hashicorp/google"}}]}}}}}}}}"#
        ),
    )
    .unwrap();
}

// Go: TestStateMoveViaMv.
#[test]
fn state_move_via_mv() {
    let root = tmp();
    write_src_plan_with_prior_state(
        root.path(),
        "stacks/a",
        "module.main.module.perms.google_project_iam_member.x",
        "google_project_iam_member",
    );
    let dst = root.path().join("stacks/b");
    mkdir(&dst);
    std::fs::write(
        dst.join("tfplan.json"),
        r#"{"format_version":"1.2","resource_changes":[]}"#,
    )
    .unwrap();
    assert!(ok(run_state(&[
        "move",
        "--dir",
        &s(root.path()),
        "--pr",
        "5",
        "--via",
        "mv",
        "stacks/a:module.main.module.perms",
        "stacks/b:module.perms",
    ])));
    let data = std::fs::read_to_string(dst.join(xmove_file_name("PR-5"))).unwrap();
    assert!(
        data.contains(r#""module.main.module.perms" = "module.perms""#),
        "{data}"
    );
    assert!(!data.contains("google_project_iam_member"), "{data}");
    assert!(!dst.join(shim_file_name("PR-5")).exists());
}

// Go: TestStateMoveViaMvFailsWhenNoPriorState.
#[test]
fn state_move_via_mv_fails_when_no_prior_state() {
    let root = tmp();
    let src = root.path().join("stacks/a");
    mkdir(&src);
    std::fs::write(
        src.join("tfplan.json"),
        r#"{"format_version":"1.2","resource_changes":[{"address":"module.perms.google_project_iam_member.x","change":{"actions":["delete"]}}]}"#,
    )
    .unwrap();
    assert!(!ok(run_state(&[
        "move",
        "--dir",
        &s(root.path()),
        "--pr",
        "5",
        "--via",
        "mv",
        "stacks/a:module.perms",
        "stacks/b:module.dest",
    ])));
}

// Go: TestStateMoveViaMvFailsWhenFromNotInPriorState.
#[test]
fn state_move_via_mv_fails_when_from_not_in_prior_state() {
    let root = tmp();
    write_src_plan_with_prior_state(
        root.path(),
        "stacks/a",
        "module.other.google_project_iam_member.x",
        "google_project_iam_member",
    );
    assert!(!ok(run_state(&[
        "move",
        "--dir",
        &s(root.path()),
        "--pr",
        "5",
        "--via",
        "mv",
        "stacks/a:module.perms",
        "stacks/b:module.dest",
    ])));
}

fn write_test_xmove(dest_dir: &Path) {
    mkdir(dest_dir);
    let xm = XMove {
        source_stack: "stacks/src".into(),
        pairs: vec![Move::new("module.x", "module.y")],
    };
    std::fs::write(
        dest_dir.join(xmove_file_name("test")),
        render_xmove("test", &xm),
    )
    .unwrap();
}

// Go: TestStateCheck_NoManifests.
#[test]
fn state_check_no_manifests() {
    let root = tmp();
    assert!(ok(run_state(&["check", "--dir", &s(root.path())])));
}

// Go: TestStateCheck_ValidManifest.
#[test]
fn state_check_valid_manifest() {
    let root = tmp();
    write_src_plan_with_prior_state(
        root.path(),
        "stacks/src",
        "module.x.google_project.p",
        "google_project",
    );
    let dest = root.path().join("stacks/dest");
    mkdir(&dest);
    std::fs::write(dest.join("providers.tf"), r#"provider "google" {}"#).unwrap();
    write_test_xmove(&dest);
    assert!(ok(run_state(&["check", "--dir", &s(root.path())])));
}

// Go: TestStateCheck_SpentManifest.
#[test]
fn state_check_spent_manifest() {
    let root = tmp();
    let dest = root.path().join("stacks/dest");
    mkdir(&dest);
    std::fs::write(
        dest.join("tfplan.json"),
        r#"{"format_version":"1.2","prior_state":{"format_version":"0.1","values":{"root_module":{"resources":[{"address":"module.y.google_project.p","type":"google_project","provider_name":"registry.terraform.io/hashicorp/google","values":{}}]}}}}"#,
    )
    .unwrap();
    write_test_xmove(&dest);
    assert!(ok(run_state(&["check", "--dir", &s(root.path())])));
}

// Go: TestStateCheck_SourceNotPlanned.
#[test]
fn state_check_source_not_planned() {
    let root = tmp();
    write_test_xmove(&root.path().join("stacks/dest"));
    assert!(!ok(run_state(&["check", "--dir", &s(root.path())])));
}

// Go: TestStateApplyDiscoversManifests: discovery and wiring only; with no real
// backend terraform fails, and the exit code is not asserted (as in Go).
#[test]
fn state_apply_discovers_manifests() {
    if terraform::look_path("terraform").is_err() {
        eprintln!("skipping: terraform not on PATH");
        return;
    }
    let root = tmp();
    let dir = root.path().join("stacks/b");
    mkdir(&dir);
    let xm = XMove {
        source_stack: "stacks/a".into(),
        pairs: vec![Move::new("x.y", "x.y")],
    };
    std::fs::write(dir.join(xmove_file_name("PR-5")), render_xmove("PR-5", &xm)).unwrap();
    let _ = run_state(&["apply", "--dir", &s(root.path())]);
}

// Go: TestStateImportAndRemove.
#[test]
fn state_import_and_remove() {
    let root = tmp();
    mkdir(&root.path().join("stacks/a"));
    let r = s(root.path());
    assert!(ok(run_state(&[
        "import",
        "--dir",
        &r,
        "--stack",
        "stacks/a",
        "--pr",
        "10",
        "aws_s3_bucket.main",
        "my-bucket"
    ])));
    let shim = shim_path(root.path(), "stacks/a", "PR-10");
    let data = std::fs::read_to_string(&shim).unwrap();
    assert!(
        data.contains("import {\n  to = aws_s3_bucket.main\n  id = \"my-bucket\""),
        "{data}"
    );
    assert!(ok(run_state(&[
        "remove",
        "--dir",
        &r,
        "--stack",
        "stacks/a",
        "--pr",
        "10",
        "aws_s3_bucket.main"
    ])));
    let data = std::fs::read_to_string(&shim).unwrap();
    assert!(
        data.contains("removed {\n  from = aws_s3_bucket.main"),
        "{data}"
    );
}

fn write_shim(root: &Path, stack: &str, key: &str, ops: &[Op]) {
    std::fs::write(
        root.join(stack).join(shim_file_name(key)),
        render_shim(key, ops),
    )
    .unwrap();
}

fn manifest(root: &Path, extra: &[&str]) -> BTreeMap<String, Vec<String>> {
    let out = root.join("manifest.json");
    let mut a = vec!["--dir", root.to_str().unwrap(), "-o", out.to_str().unwrap()];
    a.extend_from_slice(extra);
    assert!(ok(state_moves_manifest(&args(&a))));
    serde_json::from_str(&std::fs::read_to_string(out).unwrap()).unwrap()
}

// Go: TestMovesManifestTwoSided (via -o instead of capturing stdout).
#[test]
fn moves_manifest_two_sided() {
    let root = tmp();
    mkdir(&root.path().join("service-projects/svc-a"));
    mkdir(&root.path().join("cluster/svc-a"));
    write_shim(
        root.path(),
        "service-projects/svc-a",
        "PR-7",
        &[Op::removed("google_project_iam_member.node")],
    );
    write_shim(
        root.path(),
        "cluster/svc-a",
        "PR-7",
        &[Op::import(
            "google_project_iam_member.node",
            "proj roles/x sa",
        )],
    );
    let got = manifest(root.path(), &[]);
    assert_eq!(
        got["service-projects/svc-a"],
        vec!["google_project_iam_member.node"]
    );
    assert_eq!(got["cluster/svc-a"], vec!["google_project_iam_member.node"]);
}

fn write_xm(root: &Path, dest: &str, key: &str, source: &str, from: &str, to: &str) {
    let xm = XMove {
        source_stack: source.into(),
        pairs: vec![Move::new(from, to)],
    };
    std::fs::write(
        root.join(dest).join(xmove_file_name(key)),
        render_xmove(key, &xm),
    )
    .unwrap();
}

// Go: TestMovesManifestXMoveBothStacks.
#[test]
fn moves_manifest_xmove_both_stacks() {
    let root = tmp();
    mkdir(&root.path().join("src"));
    mkdir(&root.path().join("dst"));
    write_xm(
        root.path(),
        "dst",
        "PR-9",
        "src",
        "module.a.google_x.y",
        "module.b.google_x.y",
    );
    let got = manifest(root.path(), &[]);
    assert_eq!(got["src"], vec!["module.a.google_x.y"]);
    assert_eq!(got["dst"], vec!["module.b.google_x.y"]);
}

// Go: TestMovesManifestXMoveRelativeSource.
#[test]
fn moves_manifest_xmove_relative_source() {
    let root = tmp();
    mkdir(&root.path().join("stacks/tier/src"));
    mkdir(&root.path().join("stacks/tier/workloads/dst"));
    write_xm(
        root.path(),
        "stacks/tier/workloads/dst",
        "PR-42",
        "src",
        "module.x.google_y.z",
        "module.x.google_y.z",
    );
    let got = manifest(root.path(), &[]);
    assert_eq!(got["stacks/tier/src"], vec!["module.x.google_y.z"]);
    assert_eq!(
        got["stacks/tier/workloads/dst"],
        vec!["module.x.google_y.z"]
    );
}

// Go: TestMovesManifestPRFilter.
#[test]
fn moves_manifest_pr_filter() {
    let root = tmp();
    mkdir(&root.path().join("s"));
    write_shim(root.path(), "s", "PR-1", &[Op::removed("a.b")]);
    write_shim(root.path(), "s", "PR-2", &[Op::removed("c.d")]);
    let got = manifest(root.path(), &["--pr", "1"]);
    assert_eq!(got["s"], vec!["a.b"]);
}

#[test]
fn rfc3339_nano_matches_go() {
    use std::time::{Duration, UNIX_EPOCH};
    assert_eq!(rfc3339_nano_utc(UNIX_EPOCH), "1970-01-01T00:00:00Z");
    assert_eq!(
        rfc3339_nano_utc(UNIX_EPOCH + Duration::new(1_764_072_000, 120_000_000)),
        "2025-11-25T12:00:00.12Z"
    );
    assert_eq!(
        rfc3339_nano_utc(UNIX_EPOCH + Duration::new(951_782_400, 5)),
        "2000-02-29T00:00:00.000000005Z"
    );
}

#[test]
fn strip_stack_and_move_key() {
    assert_eq!(strip_stack("stacks/a:aws.x"), ("stacks/a", "aws.x"));
    assert_eq!(strip_stack("aws.x"), ("", "aws.x"));
    assert_eq!(move_key("12", "/"), "PR-12");
    // No --pr and no git repo: $TFSTACKPLAN_PR if the environment sets it, else "local".
    let k = move_key("", "/definitely/not/a/dir");
    assert!(k == "local" || k.starts_with("PR-"), "{k}");
}

#[test]
fn rand_id_is_32_hex() {
    let id = rand_id();
    assert_eq!(id.len(), 32);
    assert!(id.chars().all(|c| c.is_ascii_hexdigit()));
}
