use super::*;

fn args(a: &[&str]) -> Vec<String> {
    a.iter().map(|s| s.to_string()).collect()
}

fn whoami() -> FlagSet {
    let mut fs = FlagSet::new("whoami");
    fs.string("env", "", "environment name (optional)");
    fs
}

#[test]
fn default_usage_matches_go() {
    // Recorded from the Go binary (rust/cases/whoami-help.stderr).
    assert_eq!(
        whoami().usage(),
        "Usage of whoami:\n  -env string\n    \tenvironment name (optional)\n"
    );
}

#[test]
fn help_is_its_own_error() {
    let err = whoami().parse(&args(&["--help"])).unwrap_err();
    assert!(matches!(err, ParseError::Help(_)));
    let err = whoami().parse(&args(&["-h"])).unwrap_err();
    assert_eq!(err.stderr(), whoami().usage());
}

#[test]
fn unknown_flag_message_then_usage() {
    let err = whoami().parse(&args(&["--nope"])).unwrap_err();
    assert_eq!(
        err.stderr(),
        format!("flag provided but not defined: -nope\n{}", whoami().usage())
    );
}

#[test]
fn values_by_equals_or_next_arg_and_rest() {
    let mut fs = FlagSet::new("t");
    fs.string("dir", ".", "d")
        .bool("all", false, "a")
        .int("pr", 0, "p");
    let p = fs
        .parse(&args(&["-dir=x", "--pr", "0x1F", "-all", "a", "-b"]))
        .unwrap();
    assert_eq!(p.str("dir"), "x");
    assert_eq!(p.int("pr"), 31);
    assert!(p.bool("all"));
    assert_eq!(p.rest, args(&["a", "-b"]));
}

#[test]
fn double_dash_ends_flags() {
    let p = whoami().parse(&args(&["--", "-env", "x"])).unwrap();
    assert_eq!(p.str("env"), "");
    assert_eq!(p.rest, args(&["-env", "x"]));
}

#[test]
fn errors_match_go() {
    let mut fs = FlagSet::new("t");
    fs.int("pr", 0, "p").bool("all", false, "a");
    let first = |a: &[&str]| {
        fs.parse(&args(a))
            .unwrap_err()
            .stderr()
            .lines()
            .next()
            .unwrap()
            .to_string()
    };
    assert_eq!(first(&["-pr"]), "flag needs an argument: -pr");
    assert_eq!(
        first(&["-pr", "x"]),
        "invalid value \"x\" for flag -pr: parse error"
    );
    assert_eq!(
        first(&["-pr", "99999999999999999999"]),
        "invalid value \"99999999999999999999\" for flag -pr: value out of range"
    );
    assert_eq!(
        first(&["-all=maybe"]),
        "invalid boolean value \"maybe\" for -all: parse error"
    );
    assert_eq!(first(&["---x"]), "bad flag syntax: ---x");
    assert_eq!(first(&["-=x"]), "bad flag syntax: -=x");
}

#[test]
fn print_defaults_shows_non_zero_defaults() {
    let mut fs = FlagSet::new("t");
    fs.string("format", "text", "output format: text|json")
        .int("n", 7, "count")
        .bool("x", false, "short")
        .string("o", "", "write `FILE` here");
    assert_eq!(
        fs.print_defaults(),
        "  -format string\n    \toutput format: text|json (default \"text\")\n  -n int\n    \tcount (default 7)\n  -o FILE\n    \twrite FILE here\n  -x\tshort\n"
    );
}

#[test]
fn custom_usage_header() {
    let mut fs = FlagSet::new("t");
    fs.usage_header("Usage: t\n\nFlags:\n")
        .string("dir", "", "root");
    assert_eq!(
        fs.usage(),
        "Usage: t\n\nFlags:\n  -dir string\n    \troot\n"
    );
}
