use super::*;

#[test]
fn valid_documents_pass() {
    for s in ["{}", " [1, 2.5e-3, true, null, \"x\\u00e9\"] ", "0", "-0.1"] {
        assert_eq!(check_valid(s.as_bytes()), Ok(()), "{s}");
    }
}

#[test]
fn syntax_errors_read_like_go() {
    let cases = [
        ("", "unexpected end of JSON input"),
        ("{", "unexpected end of JSON input"),
        ("x", "invalid character 'x' looking for beginning of value"),
        (
            "{1}",
            "invalid character '1' looking for beginning of object key string",
        ),
        ("{\"a\" 1}", "invalid character '1' after object key"),
        (
            "{\"a\":1 2}",
            "invalid character '2' after object key:value pair",
        ),
        ("[1 2]", "invalid character '2' after array element"),
        ("1 2", "invalid character '2' after top-level value"),
        (
            "tru",
            "invalid character ' ' in literal true (expecting 'e')",
        ),
        (
            "trux",
            "invalid character 'x' in literal true (expecting 'e')",
        ),
        ("01", "invalid character '1' after top-level value"),
        (
            "1.",
            "invalid character ' ' after decimal point in numeric literal",
        ),
        (
            "1.x",
            "invalid character 'x' after decimal point in numeric literal",
        ),
        ("\"\\q\"", "invalid character 'q' in string escape code"),
        (
            "'a'",
            "invalid character '\\'' looking for beginning of value",
        ),
        ("\"a\nb\"", "invalid character '\\n' in string literal"),
    ];
    for (input, want) in cases {
        assert_eq!(
            check_valid(input.as_bytes()),
            Err(want.to_string()),
            "{input:?}"
        );
    }
}
