use super::*;

fn args(a: &[&str]) -> Vec<OsString> {
    a.iter().map(OsString::from).collect()
}

fn env<'a>(vars: &'a [(&'a str, &'a str)]) -> impl Fn(&str) -> Option<OsString> + 'a {
    move |k| {
        vars.iter()
            .find(|(n, _)| *n == k)
            .map(|(_, v)| OsString::from(v))
    }
}

fn scratch() -> tempfile::TempDir {
    tempfile::Builder::new()
        .prefix("tfsp-fd-")
        .tempdir()
        .unwrap()
}

#[test]
fn subcommand_matches_go_dispatch() {
    assert_eq!(subcommand(&args(&[])), "render");
    assert_eq!(subcommand(&args(&["-version"])), "render");
    assert_eq!(subcommand(&args(&["--plans-dir", "x"])), "render");
    assert_eq!(subcommand(&args(&["render", "--help"])), "render");
    assert_eq!(subcommand(&args(&["serve"])), "serve");
    assert_eq!(subcommand(&args(&["bogus"])), "bogus");
}

#[test]
fn ported_subcommands() {
    let names: Vec<&str> = PORTED.iter().map(|(n, _)| *n).collect();
    assert_eq!(names, vec!["serve", "ui"]);
    assert!(is_ported("serve"));
    assert!(!is_ported("render"));
}

#[test]
fn kill_switch_is_global_or_per_subcommand_and_ignores_case() {
    assert!(!forced_to_go("render", env(&[])));
    assert!(forced_to_go("render", env(&[("TFSTACKPLAN_IMPL", "go")])));
    assert!(forced_to_go("render", env(&[("TFSTACKPLAN_IMPL", "GO")])));
    assert!(forced_to_go(
        "render",
        env(&[("TFSTACKPLAN_IMPL_RENDER", "Go")])
    ));
    assert!(!forced_to_go(
        "serve",
        env(&[("TFSTACKPLAN_IMPL_RENDER", "go")])
    ));
    assert!(!forced_to_go(
        "render",
        env(&[("TFSTACKPLAN_IMPL", "rust")])
    ));
}

#[test]
fn a_bare_go_bin_name_is_refused() {
    let err = resolve_go_bin(Some("tfstackplan-go".into()), None).unwrap_err();
    assert_eq!(err.0, EXIT_GO_NOT_FOUND);
    assert!(err.1.contains("bare name"), "{}", err.1);
}

#[test]
fn a_missing_go_bin_is_not_found() {
    let dir = scratch();
    let missing = dir.path().join("nope");
    let err = resolve_go_bin(Some(missing.into()), None).unwrap_err();
    assert_eq!(err.0, EXIT_GO_NOT_FOUND);
    let err = resolve_go_bin(None, None).unwrap_err();
    assert_eq!(err.0, EXIT_GO_NOT_FOUND);
}

#[test]
fn the_env_path_is_used_as_given() {
    let dir = scratch();
    let go = dir.path().join("go-bin");
    std::fs::write(&go, "").unwrap();
    assert_eq!(resolve_go_bin(Some(go.clone().into()), None).unwrap(), go);
}

#[test]
fn a_go_bin_that_is_the_front_door_is_refused() {
    let dir = scratch();
    let exe = dir.path().join("tfstackplan");
    std::fs::write(&exe, "").unwrap();
    let err = resolve_go_bin(Some(exe.clone().into()), Some(&exe)).unwrap_err();
    assert_eq!(err.0, EXIT_GO_NOT_EXECUTABLE);
}

#[cfg(unix)]
#[test]
fn the_sibling_is_found_through_a_symlinked_front_door() {
    let real = scratch();
    let exe = real.path().join("tfstackplan");
    let go = real.path().join("tfstackplan-go");
    std::fs::write(&exe, "").unwrap();
    std::fs::write(&go, "").unwrap();
    let on_path = scratch();
    let link = on_path.path().join("tfstackplan");
    std::os::unix::fs::symlink(&exe, &link).unwrap();

    let found = resolve_go_bin(None, Some(&link)).unwrap();
    assert_eq!(
        std::fs::canonicalize(found).unwrap(),
        std::fs::canonicalize(go).unwrap()
    );
}
