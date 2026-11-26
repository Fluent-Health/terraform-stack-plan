use super::*;

fn have_terramate() -> bool {
    std::env::var_os("PATH")
        .is_some_and(|p| std::env::split_paths(&p).any(|d| d.join("terramate").is_file()))
}

/// A copy of the catalog replay fixture: a terramate project root outside git.
fn project() -> tempfile::TempDir {
    let src = Path::new(env!("CARGO_MANIFEST_DIR")).join("../cases/fixtures/faces/catalog/project");
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

// Go: TestTerramateList and TestTerramateRunGraph (the calls catalog makes).
#[test]
fn terramate_list_and_run_graph() {
    if !have_terramate() {
        eprintln!("skipping: terramate not on PATH");
        return;
    }
    let dir = project();
    let stacks = lines(&terramate(dir.path(), &["list"]).unwrap());
    assert_eq!(stacks, vec!["stacks/dev/app/api", "stacks/dev/app/worker"]);
    let dot = terramate(
        dir.path(),
        &["experimental", "run-graph", "-l", "stack.dir"],
    )
    .unwrap();
    let edges = tfstackplan_cli::catalog::parse_run_graph(&String::from_utf8_lossy(&dot));
    assert_eq!(
        edges,
        vec![tfstackplan_cli::catalog::StackEdge {
            from: "stacks/dev/app/api".into(),
            to: "stacks/dev/app/worker".into()
        }]
    );
}

#[test]
fn errors_read_like_go() {
    let e = std::io::Error::from(std::io::ErrorKind::NotFound);
    assert_eq!(
        start_error("terramate", &e),
        "exec: \"terramate\": executable file not found in $PATH"
    );
    assert_eq!(
        start_error("./x", &e),
        "fork/exec ./x: no such file or directory"
    );
    let dir = tempfile::tempdir().unwrap();
    if have_terramate() {
        let err = terramate(dir.path(), &["list"]).unwrap_err();
        assert!(err.starts_with("terramate list: exit status 1: "), "{err}");
    }
}

#[test]
fn lines_trims_and_drops_blanks() {
    assert_eq!(lines(b" a \n\n b\r\n"), vec!["a", "b"]);
}
