use super::*;

#[test]
fn marshal_indent_layout_matches_go() {
    let v = Value::Object(vec![
        ("a".into(), Value::strings(&["x", "y"])),
        ("b".into(), Value::Array(vec![])),
        ("c".into(), Value::Object(vec![])),
        ("d".into(), Value::Null),
    ]);
    assert_eq!(
        marshal_indent(&v, "  ").unwrap(),
        "{\n  \"a\": [\n    \"x\",\n    \"y\"\n  ],\n  \"b\": [],\n  \"c\": {},\n  \"d\": null\n}"
    );
    assert_eq!(
        marshal(&v).unwrap(),
        "{\"a\":[\"x\",\"y\"],\"b\":[],\"c\":{},\"d\":null}"
    );
}

#[test]
fn strings_are_html_safe() {
    let v = Value::str("<a&b>\u{2028}\u{1}\"\\");
    assert_eq!(
        marshal(&v).unwrap(),
        "\"\\u003ca\\u0026b\\u003e\\u2028\\u0001\\\"\\\\\""
    );
}

#[test]
fn floats_match_go() {
    let enc = |f: f64| marshal(&Value::Float(f)).unwrap();
    assert_eq!(enc(1.5), "1.5");
    assert_eq!(enc(100.0), "100");
    assert_eq!(enc(1e21), "1e+21");
    assert_eq!(enc(1e-7), "1e-7");
    assert_eq!(enc(0.000001), "0.000001");
    assert!(marshal(&Value::Float(f64::NAN)).is_err());
}
