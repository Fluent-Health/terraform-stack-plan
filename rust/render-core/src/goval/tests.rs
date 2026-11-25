use super::*;

#[test]
fn json_strings_escape_html_like_go() {
    let mut out = String::new();
    json_string(&mut out, "<a href=\"x\">&\u{2028}\u{8}\u{c}\u{1}\u{7f}");
    assert_eq!(
        out,
        "\"\\u003ca href=\\\"x\\\"\\u003e\\u0026\\u2028\\b\\f\\u0001\u{7f}\""
    );
}

#[test]
fn marshal_indent_sorts_keys_and_uses_two_spaces() {
    let v = parse_json(r#"{"b":[1,{}],"a":{"c":[]},"d":1e-7}"#).unwrap();
    assert_eq!(
        marshal_indent(&v).unwrap(),
        "{\n  \"a\": {\n    \"c\": []\n  },\n  \"b\": [\n    1,\n    {}\n  ],\n  \"d\": 1e-7\n}"
    );
}

#[test]
fn marshal_indent_rejects_what_go_rejects() {
    assert!(marshal_indent(&Value::Float(f64::NAN)).is_none());
    assert!(marshal_indent(&Value::AnyMap(vec![(Value::Int(1), Value::Null)])).is_none());
}

#[test]
fn json_numbers_decode_as_float64() {
    assert_eq!(
        parse_json("12345678901234567890").unwrap(),
        Value::Float(12345678901234567890.0)
    );
    assert_eq!(
        parse_json("[1]").unwrap(),
        Value::Seq(vec![Value::Float(1.0)])
    );
}
