use super::*;

// Go: TestDiscoverWalksUpToRepoRoot.
#[test]
fn discover_walks_up_to_repo_root() {
    let root = tempfile::tempdir().unwrap();
    std::fs::create_dir(root.path().join(".git")).unwrap();
    let cfg = root.path().join(config::DEFAULT_FILENAME);
    std::fs::write(&cfg, "classification {}\n").unwrap();
    let sub = root.path().join("stacks/prod");
    std::fs::create_dir_all(&sub).unwrap();
    assert_eq!(discover_config(&sub), Some(cfg));

    // A config above the repo root is out of scope.
    let above = tempfile::tempdir().unwrap();
    std::fs::write(
        above.path().join(config::DEFAULT_FILENAME),
        "classification {}\n",
    )
    .unwrap();
    let repo = above.path().join("repo");
    std::fs::create_dir_all(repo.join(".git")).unwrap();
    let deep = repo.join("a/b");
    std::fs::create_dir_all(&deep).unwrap();
    assert_eq!(discover_config(&deep), None);
}

#[test]
fn os_errors_read_like_go() {
    let e = std::fs::read("/definitely/not/here").unwrap_err();
    assert_eq!(
        os_error("open", Path::new("/definitely/not/here"), &e),
        "open /definitely/not/here: no such file or directory"
    );
}
