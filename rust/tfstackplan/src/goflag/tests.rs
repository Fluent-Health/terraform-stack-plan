use super::*;

const FLAGS: &[(&str, Kind)] = &[
    ("name", Kind::Str),
    ("n", Kind::Int),
    ("v", Kind::Bool),
    ("l", Kind::List),
];

fn args(a: &[&str]) -> Vec<String> {
    a.iter().map(|s| s.to_string()).collect()
}

#[test]
fn single_and_double_dash_with_or_without_equals() {
    let (v, rest) = parse(
        &args(&[
            "-name=a", "--n", "0x10", "-l", "x", "--l=y", "-v", "pos", "-name=b",
        ]),
        FLAGS,
    )
    .unwrap();
    assert_eq!(v["name"], vec!["a"]);
    assert_eq!(v["n"], vec!["0x10"]);
    assert_eq!(v["l"], vec!["x", "y"]);
    assert_eq!(v["v"], vec!["true"]);
    assert_eq!(rest, args(&["pos", "-name=b"]));
}

#[test]
fn double_dash_ends_flags() {
    let (v, rest) = parse(&args(&["-name", "a", "--", "-v"]), FLAGS).unwrap();
    assert_eq!(v["name"], vec!["a"]);
    assert!(!v.contains_key("v"));
    assert_eq!(rest, args(&["-v"]));
}

#[test]
fn errors_read_like_go() {
    let err = |a: &[&str]| parse(&args(a), FLAGS).unwrap_err();
    assert_eq!(
        err(&["---x"]),
        Stop::Error("bad flag syntax: ---x".to_string())
    );
    assert_eq!(
        err(&["-=x"]),
        Stop::Error("bad flag syntax: -=x".to_string())
    );
    assert_eq!(
        err(&["--nope"]),
        Stop::Error("flag provided but not defined: -nope".to_string())
    );
    assert_eq!(
        err(&["-name"]),
        Stop::Error("flag needs an argument: -name".to_string())
    );
    assert_eq!(
        err(&["-n", "x"]),
        Stop::Error("invalid value \"x\" for flag -n: parse error".to_string())
    );
    assert_eq!(
        err(&["-n=99999999999999999999"]),
        Stop::Error(
            "invalid value \"99999999999999999999\" for flag -n: value out of range".to_string()
        )
    );
    assert_eq!(
        err(&["-v=maybe"]),
        Stop::Error("invalid boolean value \"maybe\" for -v: parse error".to_string())
    );
    assert_eq!(err(&["--help"]), Stop::Help);
    assert_eq!(err(&["-h"]), Stop::Help);
}

#[test]
fn parse_int_like_strconv_base_0() {
    assert_eq!(parse_int("42"), Ok(42));
    assert_eq!(parse_int("-0x1F"), Ok(-31));
    assert_eq!(parse_int("0o17"), Ok(15));
    assert_eq!(parse_int("017"), Ok(15));
    assert_eq!(parse_int("0b101"), Ok(5));
    assert_eq!(parse_int("1_000"), Ok(1000));
    assert_eq!(parse_int("1__0"), Err("parse error"));
    assert_eq!(parse_int("0x"), Err("parse error"));
    assert_eq!(parse_int("08"), Err("parse error"));
    assert_eq!(parse_int("-9223372036854775808"), Ok(i64::MIN));
    assert_eq!(parse_int("9223372036854775808"), Err("value out of range"));
}

#[test]
fn parse_bool_like_strconv() {
    assert_eq!(parse_bool("T"), Some(true));
    assert_eq!(parse_bool("0"), Some(false));
    assert_eq!(parse_bool("yes"), None);
}
