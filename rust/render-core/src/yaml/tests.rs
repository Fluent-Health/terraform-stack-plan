use std::collections::BTreeMap;

use super::*;

fn s(v: &str) -> Value {
    Value::Str(v.to_string())
}

fn m(pairs: Vec<(&str, Value)>) -> Value {
    Value::Map(pairs.into_iter().map(|(k, v)| (k.to_string(), v)).collect())
}

#[test]
fn dump_nested_block_styles() {
    let v = m(vec![
        (
            "b",
            Value::Seq(vec![
                s("x"),
                m(vec![("k", Value::Int(1)), ("j", Value::Bool(true))]),
            ]),
        ),
        (
            "a",
            m(vec![
                ("c", Value::Null),
                ("e", Value::Seq(vec![])),
                ("f", m(vec![])),
            ]),
        ),
    ]);
    assert_eq!(
        dump(&v),
        "a:\n    c: null\n    e: []\n    f: {}\nb:\n    - x\n    - j: true\n      k: 1\n"
    );
}

#[test]
fn dump_quotes_strings_that_would_resolve_otherwise() {
    let v = m(vec![
        ("bool", s("yes")),
        ("date", s("2024-01-01")),
        ("num", s("012")),
        ("empty", s("")),
        ("colon", s("a: b")),
        ("lead", s(" x")),
        ("plain", s("hello world")),
        ("null", s("null")),
    ]);
    assert_eq!(
        dump(&v),
        "bool: \"yes\"\ncolon: 'a: b'\ndate: \"2024-01-01\"\nempty: \"\"\nlead: ' x'\n\"null\": \"null\"\nnum: \"012\"\nplain: hello world\n"
    );
}

#[test]
fn dump_multiline_strings_as_literal_blocks() {
    let v = m(vec![
        ("a", s("line1\nline2")),
        ("b", s("x\n")),
        ("c", s("x\n\n")),
    ]);
    assert_eq!(
        dump(&v),
        "a: |-\n    line1\n    line2\nb: |\n    x\nc: |+\n    x\n\n"
    );
}

#[test]
fn dump_never_folds_long_scalars() {
    // yaml.v3 sets best_width to -1: no line folding at 80 columns.
    let long = vec!["word"; 40].join(" ");
    let out = dump(&m(vec![("k", s(&long))]));
    assert_eq!(out, format!("k: {long}\n"));
}

#[test]
fn dump_escapes_supplementary_characters() {
    // libyaml's is_printable stops at U+FFFF, so emoji are double-quoted and escaped.
    assert_eq!(dump(&m(vec![("k", s("hi 😀"))])), "k: \"hi \\U0001F600\"\n");
    assert_eq!(dump(&m(vec![("k", s("é日本"))])), "k: é日本\n");
}

#[test]
fn dump_floats_and_natural_key_order() {
    let v = m(vec![
        ("a10", Value::Float(1e6)),
        ("a9", Value::Float(0.5)),
        ("a2", Value::Float(3.0)),
    ]);
    assert_eq!(dump(&v), "a2: 3\na9: 0.5\na10: 1e+06\n");
}

#[test]
fn load_resolves_scalars_like_v3() {
    let v = load("a: 0777\nb: 0x1F\nc: 1_000\nd: yes\ne: ~\nf: 1.5\ng: 08\nh: '1'\ni: 18446744073709551615\n").unwrap();
    let Value::Map(map) = v else {
        panic!("not a map")
    };
    assert_eq!(map["a"], Value::Int(511));
    assert_eq!(map["b"], Value::Int(31));
    assert_eq!(map["c"], Value::Int(1000));
    assert_eq!(map["d"], s("yes"));
    assert_eq!(map["e"], Value::Null);
    assert_eq!(map["f"], Value::Float(1.5));
    assert_eq!(map["g"], Value::Float(8.0));
    assert_eq!(map["h"], s("1"));
    assert_eq!(map["i"], Value::Uint(u64::MAX));
}

#[test]
fn load_timestamps_round_trip_as_rfc3339() {
    let v = load("t: 2024-01-02\nu: 2024-1-2T3:04:05.120+05:30\nv: 2024-02-30\n").unwrap();
    let Value::Map(map) = v else {
        panic!("not a map")
    };
    let Value::Time(t) = &map["t"] else {
        panic!("t")
    };
    assert_eq!(t.rfc3339_nano(), "2024-01-02T00:00:00Z");
    let Value::Time(u) = &map["u"] else {
        panic!("u")
    };
    assert_eq!(u.rfc3339_nano(), "2024-01-02T03:04:05.12+05:30");
    assert_eq!(map["v"], s("2024-02-30"));
}

#[test]
fn load_merge_keys_and_non_string_keys() {
    let v = load("base: &b {x: 1, y: 2}\nd:\n  <<: *b\n  y: 3\n").unwrap();
    let Value::Map(map) = v else {
        panic!("not a map")
    };
    let want: BTreeMap<String, Value> = [
        ("x".to_string(), Value::Int(1)),
        ("y".to_string(), Value::Int(3)),
    ]
    .into_iter()
    .collect();
    assert_eq!(map["d"], Value::Map(want));
    let any = load("1: a\nb: c\n").unwrap();
    assert!(matches!(any, Value::AnyMap(_)));
}

#[test]
fn load_rejects_duplicate_keys_and_reads_first_document() {
    assert!(load("a: 1\na: 2\n").is_err());
    assert_eq!(load("").unwrap(), Value::Null);
    assert_eq!(load("a\n---\nb: [\n").unwrap(), s("a"));
}
