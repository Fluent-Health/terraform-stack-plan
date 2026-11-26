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
fn ported_subcommands() {
    let names: Vec<&str> = PORTED.iter().map(|(n, _)| *n).collect();
    assert_eq!(names, vec!["serve", "ui"]);
}
