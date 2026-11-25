use super::*;

fn args(a: &[&str]) -> Vec<OsString> {
    a.iter().map(OsString::from).collect()
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
fn ported_faces_and_run_subcommands() {
    let find = |a: &[&str]| ported(&subcommand(&args(a)), &args(a)).map(|(_, skip)| skip);
    assert_eq!(find(&["state", "list"]), Some(1));
    assert_eq!(find(&["whoami"]), Some(1));
    assert_eq!(find(&["run", "wrap", "--", "true"]), Some(2));
    assert_eq!(find(&["run", "claims", "list"]), Some(2));
    assert_eq!(find(&["run", "plan"]), None);
    assert_eq!(find(&["run"]), None);
    assert_eq!(find(&["render"]), Some(1));
    assert_eq!(find(&["--plans-dir", "x"]), Some(0));
    assert_eq!(find(&[]), Some(0));
    assert_eq!(find(&["serve"]), None);
}
