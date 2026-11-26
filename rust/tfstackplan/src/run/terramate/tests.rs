//! The real driver against real terramate (skipped when it is not on PATH), on a
//! fixture project; `--changed` needs git and is exercised in the replay cases.

use super::*;

fn have(bin: &str) -> bool {
    std::env::var_os("PATH")
        .is_some_and(|p| std::env::split_paths(&p).any(|d| d.join(bin).is_file()))
}

fn project() -> tempfile::TempDir {
    let src = std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../cases/fixtures/faces/catalog/project");
    let dst = tempfile::tempdir().unwrap();
    for rel in [
        "terramate.tm.hcl",
        "stacks/dev/app/api/stack.tm.hcl",
        "stacks/dev/app/worker/stack.tm.hcl",
    ] {
        let to = dst.path().join(rel);
        std::fs::create_dir_all(to.parent().unwrap()).unwrap();
        std::fs::copy(src.join(rel), to).unwrap();
    }
    dst
}

// Go: TestTerramateList, TestTerramateRunGraph.
#[test]
fn list_and_run_graph() {
    if !have("terramate") {
        eprintln!("skipping: terramate not on PATH");
        return;
    }
    let dir = project();
    let tm = Real::new(&dir.path().to_string_lossy());
    assert_eq!(tm.list().unwrap(), vec!["stacks/dev/app/api", "stacks/dev/app/worker"]);
    assert_eq!(
        tm.run_graph().unwrap(),
        vec![Edge {
            from: "stacks/dev/app/api".into(),
            to: "stacks/dev/app/worker".into()
        }]
    );
}

// Go: TestTerramateChangedStacks and TestTerramateWhyChanged need a git history;
// here the error path: no git repository is a terramate error, worded like Go's.
#[test]
fn changed_without_git_is_an_error() {
    if !have("terramate") || have("git") {
        eprintln!("skipping: needs terramate without git");
        return;
    }
    let dir = project();
    let err = Real::new(&dir.path().to_string_lossy()).changed_stacks("").unwrap_err();
    assert!(err.starts_with("terramate list --changed: exit status 1: "), "{err}");
}
