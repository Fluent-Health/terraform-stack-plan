// Go's encoder and decoder behaviour the handlers rely on, pinned against the
// strings Go 1.25 produces.
use tfstackplan_gate::events::{GateCheck, Init, Update};

use super::shape::{GATE_CHECK, INIT, UPDATE};
use super::*;

#[test]
fn encode_escapes_html_and_appends_newline() {
    let got = encode(&serde_json::json!({"a": "<b>&\u{2028}🔐—"}));
    assert_eq!(
        got,
        "{\"a\":\"\\u003cb\\u003e\\u0026\\u2028🔐—\"}\n".as_bytes()
    );
}

#[test]
fn decode_empty_body_is_eof() {
    assert_eq!(decode::<GateCheck>(b"", &GATE_CHECK).unwrap_err(), "EOF");
    assert_eq!(
        decode::<GateCheck>(b"  \n", &GATE_CHECK).unwrap_err(),
        "EOF"
    );
}

#[test]
fn decode_truncated_is_unexpected_eof() {
    assert_eq!(
        decode::<GateCheck>(br#"{"pr":1"#, &GATE_CHECK).unwrap_err(),
        "unexpected EOF"
    );
}

#[test]
fn decode_syntax_error_text() {
    assert_eq!(
        decode::<GateCheck>(b"nope", &GATE_CHECK).unwrap_err(),
        "invalid character 'o' in literal null (expecting 'u')"
    );
    assert_eq!(
        decode::<GateCheck>(b"{x}", &GATE_CHECK).unwrap_err(),
        "invalid character 'x' looking for beginning of object key string"
    );
}

#[test]
fn decode_ignores_trailing_data() {
    let g: GateCheck = decode(br#"{"pr":7,"environment":"s"} trailing"#, &GATE_CHECK).unwrap();
    assert_eq!(g.pr, 7);
}

#[test]
fn decode_matches_keys_case_insensitively_and_skips_nulls() {
    let g: GateCheck = decode(br#"{"PR":7,"Environment":null}"#, &GATE_CHECK).unwrap();
    assert_eq!(g.pr, 7);
    assert_eq!(g.environment, "");
}

#[test]
fn decode_type_mismatch_names_the_go_field() {
    assert_eq!(
        decode::<GateCheck>(br#"{"pr":"7"}"#, &GATE_CHECK).unwrap_err(),
        "json: cannot unmarshal string into Go struct field GateCheck.pr of type int"
    );
    assert_eq!(
        decode::<Init>(br#"{"stacks":[{"path":3}]}"#, &INIT).unwrap_err(),
        "json: cannot unmarshal number into Go struct field StackState.stacks.path of type string"
    );
    assert_eq!(
        decode::<GateCheck>(br#"{"pr":1.5}"#, &GATE_CHECK).unwrap_err(),
        "json: cannot unmarshal number 1.5 into Go struct field GateCheck.pr of type int"
    );
}

#[test]
fn decode_unknown_status_is_the_coded_error() {
    assert_eq!(
        decode::<Update>(br#"{"id":"e1","stack":"a","status":"bogus"}"#, &UPDATE).unwrap_err(),
        "WIRE-001: unknown stack status \"bogus\""
    );
}

#[test]
fn first_value_of_a_scalar_stops_at_the_delimiter() {
    assert_eq!(first_value(b"12 34").unwrap(), b"12");
    assert_eq!(first_value(b"\"a\"x").unwrap(), b"\"a\"");
    assert_eq!(first_value(b"[1]]").unwrap(), b"[1]");
    assert_eq!(first_value(b"12").unwrap(), b"12");
}
