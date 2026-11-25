use std::path::Path;

use tfstackplan_cli::statemove::{
    Move, Op, XMove, render_shim, render_xmove, shim_file_name, xmove_file_name,
};

use super::*;

fn root() -> tempfile::TempDir {
    tempfile::Builder::new()
        .prefix("tfsp-walk-")
        .tempdir()
        .unwrap()
}

fn s(p: &Path) -> String {
    p.to_string_lossy().into_owned()
}

fn write_shim(dir: &Path, key: &str, ops: &[Op]) {
    std::fs::create_dir_all(dir).unwrap();
    std::fs::write(dir.join(shim_file_name(key)), render_shim(key, ops)).unwrap();
}

fn write_xmove(dir: &Path, key: &str, xm: &XMove) {
    std::fs::create_dir_all(dir).unwrap();
    std::fs::write(dir.join(xmove_file_name(key)), render_xmove(key, xm)).unwrap();
}

fn xm(source: &str, from: &str, to: &str) -> XMove {
    XMove {
        source_stack: source.into(),
        pairs: vec![Move::new(from, to)],
    }
}

// Go: TestDiscoverAndCleanup.
#[test]
fn discover_and_cleanup() {
    let r = root();
    let a = r.path().join("stacks/a");
    write_shim(&a, "PR-1", &[Op::moved("x.a", "x.b")]);
    write_shim(&a, "PR-2", &[Op::moved("y.a", "y.b")]);
    write_shim(
        &r.path().join("stacks/b"),
        "PR-1",
        &[Op::moved("z.a", "z.b")],
    );
    std::fs::write(a.join("main.tf"), "resource \"x\" \"y\" {}").unwrap();

    let all = discover(&s(r.path())).unwrap();
    assert_eq!(all.len(), 3);
    assert_eq!(all[0].stack, "stacks/a");
    assert_eq!(cleanup(&s(r.path()), "PR-1").unwrap(), 2);
    let left = discover(&s(r.path())).unwrap();
    assert_eq!(left.len(), 1);
    assert_eq!(left[0].key, "PR-2");
    assert_eq!(cleanup(&s(r.path()), "").unwrap(), 1);
    assert!(discover(&s(r.path())).unwrap().is_empty());
}

// Go: TestDiscoverErrorsOnCorruptShim.
#[test]
fn discover_errors_on_corrupt_shim() {
    let r = root();
    let a = r.path().join("stacks/a");
    std::fs::create_dir_all(&a).unwrap();
    std::fs::write(a.join(shim_file_name("PR-1")), "not a shim\n").unwrap();
    let e = discover(&s(r.path())).unwrap_err();
    assert!(
        e.ends_with("_tfsp_move.PR-1.tf: not a tfstackplan state-move shim"),
        "{e}"
    );
}

// Go: TestDiscoverErrorsOnKeyMismatch.
#[test]
fn discover_errors_on_key_mismatch() {
    let r = root();
    let a = r.path().join("stacks/a");
    std::fs::create_dir_all(&a).unwrap();
    std::fs::write(
        a.join(shim_file_name("PR-1")),
        render_shim("PR-2", &[Op::moved("x.a", "x.b")]),
    )
    .unwrap();
    let e = discover(&s(r.path())).unwrap_err();
    assert!(
        e.ends_with("key mismatch (filename \"PR-1\" != header \"PR-2\")"),
        "{e}"
    );
}

// Go: TestCleanupRemovesCorruptShimByFilename.
#[test]
fn cleanup_removes_corrupt_shim_by_filename() {
    let r = root();
    let a = r.path().join("stacks/a");
    std::fs::create_dir_all(&a).unwrap();
    std::fs::write(a.join(shim_file_name("PR-1")), "garbage\n").unwrap();
    assert_eq!(cleanup(&s(r.path()), "PR-1").unwrap(), 1);
    assert!(!a.join(shim_file_name("PR-1")).exists());
}

// Go: TestCleanupAllRemovesCorruptAndValid.
#[test]
fn cleanup_all_removes_corrupt_and_valid() {
    let r = root();
    let a = r.path().join("stacks/a");
    write_shim(&a, "PR-1", &[Op::moved("x.a", "x.b")]);
    std::fs::write(a.join(shim_file_name("PR-9")), "garbage\n").unwrap();
    assert_eq!(cleanup(&s(r.path()), "").unwrap(), 2);
}

// Go: TestCleanupReachesKeyMismatchedShim.
#[test]
fn cleanup_reaches_key_mismatched_shim() {
    let r = root();
    let a = r.path().join("stacks/a");
    std::fs::create_dir_all(&a).unwrap();
    std::fs::write(
        a.join(shim_file_name("PR-1")),
        render_shim("PR-2", &[Op::moved("x.a", "x.b")]),
    )
    .unwrap();
    assert_eq!(cleanup(&s(r.path()), "PR-1").unwrap(), 1);
}

// Go: TestCleanupXMovesRemovesCorruptByFilename.
#[test]
fn cleanup_xmoves_removes_corrupt_by_filename() {
    let r = root();
    let d = r.path().join("stacks/dst");
    std::fs::create_dir_all(&d).unwrap();
    std::fs::write(d.join(xmove_file_name("PR-1")), "garbage\n").unwrap();
    assert_eq!(cleanup_xmoves(&s(r.path()), "PR-1").unwrap(), 1);
}

// Go: TestDiscoverAndCleanupXMoves.
#[test]
fn discover_and_cleanup_xmoves() {
    let r = root();
    let x = xm("src", "a.b", "a.b");
    write_xmove(&r.path().join("stacks/dst1"), "PR-5", &x);
    write_xmove(&r.path().join("stacks/dst2"), "PR-5", &x);
    write_xmove(&r.path().join("stacks/dst3"), "PR-6", &x);
    assert_eq!(discover_xmoves(&s(r.path())).unwrap().len(), 3);
    assert_eq!(cleanup_xmoves(&s(r.path()), "PR-5").unwrap(), 2);
    let left = discover_xmoves(&s(r.path())).unwrap();
    assert_eq!(left.len(), 1);
    assert_eq!(left[0].key, "PR-6");
    assert_eq!(cleanup_xmoves(&s(r.path()), "").unwrap(), 1);
    assert!(discover_xmoves(&s(r.path())).unwrap().is_empty());
}

// Go: TestDiscoverXMovesErrorsOnCorruptManifest.
#[test]
fn discover_xmoves_errors_on_corrupt_manifest() {
    let r = root();
    let d = r.path().join("stacks/dst");
    std::fs::create_dir_all(&d).unwrap();
    std::fs::write(
        d.join(xmove_file_name("PR-1")),
        "# tfstackplan:key=PR-1\nxmove { not valid hcl\n",
    )
    .unwrap();
    assert!(discover_xmoves(&s(r.path())).is_err());
}

// Go: TestDiscoverXMovesErrorsOnKeyMismatch.
#[test]
fn discover_xmoves_errors_on_key_mismatch() {
    let r = root();
    let d = r.path().join("stacks/dst");
    std::fs::create_dir_all(&d).unwrap();
    std::fs::write(
        d.join(xmove_file_name("PR-1")),
        render_xmove("PR-2", &xm("src", "a.b", "a.b")),
    )
    .unwrap();
    assert!(
        discover_xmoves(&s(r.path()))
            .unwrap_err()
            .contains("key mismatch")
    );
}

// Go: TestDiscoverXMoves.
#[test]
fn discover_xmoves_found() {
    let r = root();
    write_xmove(
        &r.path().join("stacks/b"),
        "PR-5",
        &xm("stacks/a", "x.y", "x.y"),
    );
    let found = discover_xmoves(&s(r.path())).unwrap();
    assert_eq!(found.len(), 1);
    assert_eq!(found[0].key, "PR-5");
    assert_eq!(found[0].dest_stack, "stacks/b");
    assert_eq!(found[0].xmove.source_stack, "stacks/a");
}

// Go: TestDiscoverXMovesAcceptsManifestWithoutKeyHeader.
#[test]
fn discover_xmoves_accepts_manifest_without_key_header() {
    let r = root();
    let d = r.path().join("stacks/dst");
    std::fs::create_dir_all(&d).unwrap();
    let content = "xmove {\n  source_stack = \"stacks/a\"\n  moves = {\n    \"aws_s3_bucket.x\" = \"aws_s3_bucket.x\"\n  }\n}\n";
    std::fs::write(d.join(xmove_file_name("PR-7")), content).unwrap();
    let found = discover_xmoves(&s(r.path())).unwrap();
    assert_eq!(found.len(), 1);
    assert_eq!(found[0].key, "PR-7");
    assert_eq!(found[0].xmove.source_stack, "stacks/a");
}

#[test]
fn missing_root_reads_like_go() {
    assert_eq!(
        discover("/definitely/not/here").unwrap_err(),
        "lstat /definitely/not/here: no such file or directory"
    );
}

// Go: TestDiscoverDestProviders (the directory read).
#[test]
fn tf_files_reads_only_tf_files_in_name_order() {
    let r = root();
    std::fs::write(r.path().join("b.tf"), "provider \"postgresql\" {}").unwrap();
    std::fs::write(r.path().join("a.tf"), "provider \"google\" {\n}").unwrap();
    std::fs::write(r.path().join("c.hcl"), "provider \"aws\" {}").unwrap();
    std::fs::create_dir(r.path().join("d.tf")).unwrap();
    let files = tf_files(&s(r.path()));
    assert_eq!(files.len(), 2);
    let p = tfstackplan_cli::statemove::dest_providers(files.iter().map(String::as_str));
    assert_eq!(
        p.into_iter().collect::<Vec<_>>(),
        vec!["google", "postgresql"]
    );
}
