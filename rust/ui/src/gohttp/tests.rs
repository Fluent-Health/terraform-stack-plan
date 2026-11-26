use axum::http::{HeaderMap, HeaderValue};

use super::*;

#[test]
fn cookies_like_go() {
    assert_eq!(
        set_cookie_line("n", "a b", "/auth", 600),
        "n=\"a b\"; Path=/auth; Max-Age=600; HttpOnly; Secure; SameSite=Lax"
    );
    assert_eq!(
        set_cookie_line("n", "x\"y;z\u{e9}", "/", 0),
        "n=xyz; Path=/; HttpOnly; Secure; SameSite=Lax"
    );
    let mut h = HeaderMap::new();
    h.append(
        "cookie",
        HeaderValue::from_static("a=1; bad name=2; n=\"q\"; n=second"),
    );
    assert_eq!(cookie(&h, "n").as_deref(), Some("q"));
    assert_eq!(cookie(&h, "a").as_deref(), Some("1"));
    assert_eq!(cookie(&h, "missing"), None);
    let mut h = HeaderMap::new();
    h.append("cookie", HeaderValue::from_static("n=a\\b; n=ok"));
    assert_eq!(
        cookie(&h, "n").as_deref(),
        Some("ok"),
        "an invalid value is skipped"
    );
}

#[test]
fn queries_like_go() {
    assert_eq!(query_get_str("a=1&a=2&b=x+y%21", "a"), "1");
    assert_eq!(query_get_str("a=1&a=2&b=x+y%21", "b"), "x y!");
    assert_eq!(query_values("a=1;b&a=%zz&a=3", "a"), vec![b"3".to_vec()]);
}

#[test]
fn escaping_like_go() {
    assert_eq!(
        escape(b"a b/c;d,e?f", Mode::PathSegment),
        "a%20b%2Fc%3Bd%2Ce%3Ff"
    );
    assert_eq!(escape(b"/a b/c;d", Mode::Path), "/a%20b/c;d");
    assert_eq!(escape(b"a b&c=d", Mode::QueryComponent), "a+b%26c%3Dd");
    assert_eq!(
        unescape(b"%zz", Mode::PathSegment),
        Err("invalid URL escape \"%zz\"".into())
    );
    assert_eq!(
        unescape(b"ab%4", Mode::PathSegment),
        Err("invalid URL escape \"%4\"".into())
    );
    assert_eq!(unescape(b"a+b", Mode::PathSegment), Ok(b"a+b".to_vec()));
}

#[test]
fn paths_like_go() {
    for (in_, want) in [
        ("", "/"),
        ("/", "/"),
        ("/a/../b/", "/b/"),
        ("//x", "/x"),
        ("/a/./b/..", "/a"),
        ("/..", "/"),
    ] {
        assert_eq!(clean_path(in_), want, "{in_}");
    }
    assert_eq!(path_base("/a/b"), "b");
    assert_eq!(path_base("/."), ".");
}

#[test]
fn quote_and_ints_like_go() {
    assert_eq!(
        go_quote(b"a\"b\\\n\x01\xff\xc3\xa9"),
        "\"a\\\"b\\\\\\n\\x01\\xff\u{e9}\""
    );
    assert_eq!(parse_int(b"+7"), Ok(7));
    assert_eq!(
        parse_int(b""),
        Err(
            "error binding string parameter: strconv.ParseInt: parsing \"\": invalid syntax".into()
        )
    );
}

#[test]
fn json_and_html_like_go() {
    assert_eq!(go_json("<a>&\u{2028}"), "\"\\u003ca\\u003e\\u0026\\u2028\"");
    assert_eq!(
        html_escape("<'\"&+\0>"),
        "&lt;&#39;&#34;&amp;&#43;\u{FFFD}&gt;"
    );
    assert_eq!(sniff(b"<!DOCTYPE html><p>"), "text/html; charset=utf-8");
    assert_eq!(sniff(b"hello"), "text/plain; charset=utf-8");
    assert_eq!(sniff(b"\x00\x01"), "application/octet-stream");
}
