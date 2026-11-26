use super::*;

// Go: TestDiscoverEnvironments.
#[test]
fn discover_environments_from_config_then_stacks() {
    let tmp = tempfile::tempdir().unwrap();
    std::fs::write(
        tmp.path().join(".tfstackplan.hcl"),
        "server \"dev\" {\n  url = \"https://dev-srv\"\n}\nserver \"prod\" {\n  url = \"https://prod-srv\"\n}\n",
    )
    .unwrap();
    assert_eq!(discover_environments(tmp.path()), vec!["dev", "prod"]);

    std::fs::create_dir_all(tmp.path().join("stacks/staging")).unwrap();
    assert_eq!(discover_environments(tmp.path()), vec!["staging"]);
}

// Go: TestDispatchClaimsBase.
#[test]
fn claims_base_usage_errors() {
    let args = |a: &[&str]| a.iter().map(|s| s.to_string()).collect::<Vec<_>>();
    assert_eq!(run(&args(&[])), ExitCode::from(2));
    assert_eq!(run(&args(&["invalid"])), ExitCode::from(2));
    assert_eq!(run(&args(&["release", "--env", "dev"])), ExitCode::from(2));
}
