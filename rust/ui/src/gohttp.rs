//! Go's `net/http`, `net/url` and `html/template` behavior the ui face shows on the wire:
//! `http.Error`, `http.Redirect`, cookies as `http.SetCookie` writes and `r.Cookie` reads
//! them, `url.ParseQuery`, the URL escapers, `path.Clean`, `strconv.Quote`, the
//! `encoding/json` escaping and content sniffing. Everything works on bytes where Go's
//! strings may hold bytes that are not UTF-8.

use axum::body::Body;
use axum::http::{HeaderMap, HeaderValue, Method, Response, StatusCode, header};
use serde::Serialize;

/// A response with the ui's body type.
pub type Resp = Response<Body>;

/// A response with `status`, no headers and `body`.
pub fn status(status: u16, body: impl Into<Body>) -> Resp {
    let mut r = Response::new(body.into());
    *r.status_mut() = StatusCode::from_u16(status).unwrap_or(StatusCode::INTERNAL_SERVER_ERROR);
    r
}

/// Sets a header, dropping values that are not valid header text.
pub fn set_header(h: &mut HeaderMap, name: &'static str, value: &str) {
    if let Ok(v) = HeaderValue::from_str(value) {
        h.insert(name, v);
    }
}

/// Go's `http.Error`: text/plain, nosniff, the message and a newline.
pub fn error(code: u16, msg: &str) -> Resp {
    let mut r = status(code, format!("{msg}\n"));
    let h = r.headers_mut();
    h.insert(
        header::CONTENT_TYPE,
        HeaderValue::from_static("text/plain; charset=utf-8"),
    );
    h.insert(
        header::X_CONTENT_TYPE_OPTIONS,
        HeaderValue::from_static("nosniff"),
    );
    r
}

/// Go's `StatusText` for the codes the ui writes itself.
pub fn status_text(code: u16) -> &'static str {
    match code {
        301 => "Moved Permanently",
        302 => "Found",
        405 => "Method Not Allowed",
        _ => "",
    }
}

/// Go's `http.Redirect` for an absolute URL or an absolute path: Location (non-ASCII
/// hex-escaped), and for GET and HEAD an HTML content type, with a link body on GET.
pub fn redirect(method: &Method, url: &str, code: u16) -> Resp {
    let loc = hex_escape_non_ascii(url);
    let get = method == Method::GET;
    let body = if get {
        format!(
            "<a href=\"{}\">{}</a>.\n\n",
            net_http_html_escape(&loc),
            status_text(code)
        )
    } else {
        String::new()
    };
    let mut r = status(code, body);
    let h = r.headers_mut();
    set_header(h, "location", &loc);
    if get || method == Method::HEAD {
        h.insert(
            header::CONTENT_TYPE,
            HeaderValue::from_static("text/html; charset=utf-8"),
        );
    }
    r
}

/// net/http's `hexEscapeNonASCII`.
fn hex_escape_non_ascii(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    for b in s.bytes() {
        if b >= 0x80 {
            out.push_str(&format!("%{b:02x}"));
        } else {
            out.push(char::from(b));
        }
    }
    out
}

/// net/http's `htmlEscape` (the redirect body's).
fn net_http_html_escape(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    for c in s.chars() {
        match c {
            '&' => out.push_str("&amp;"),
            '<' => out.push_str("&lt;"),
            '>' => out.push_str("&gt;"),
            '"' => out.push_str("&#34;"),
            '\'' => out.push_str("&#39;"),
            c => out.push(c),
        }
    }
    out
}

/// `html/template`'s escaper for text and quoted attribute values.
pub fn html_escape(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    for c in s.chars() {
        match c {
            '\0' => out.push('\u{FFFD}'),
            '"' => out.push_str("&#34;"),
            '&' => out.push_str("&amp;"),
            '\'' => out.push_str("&#39;"),
            '+' => out.push_str("&#43;"),
            '<' => out.push_str("&lt;"),
            '>' => out.push_str("&gt;"),
            c => out.push(c),
        }
    }
    out
}

/// `encoding/json`'s HTML-safe escaping applied to serde_json's output: `<`, `>`, `&`,
/// U+2028 and U+2029 as `\u` escapes. Those characters only occur inside strings.
fn go_escape_json(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    for c in s.chars() {
        match c {
            '<' => out.push_str("\\u003c"),
            '>' => out.push_str("\\u003e"),
            '&' => out.push_str("\\u0026"),
            '\u{2028}' => out.push_str("\\u2028"),
            '\u{2029}' => out.push_str("\\u2029"),
            c => out.push(c),
        }
    }
    out
}

/// Go's `json.Marshal`.
pub fn go_json<T: Serialize + ?Sized>(v: &T) -> String {
    go_escape_json(&serde_json::to_string(v).unwrap_or_default())
}

/// `w.Header().Set("Content-Type", "application/json")` and `json.NewEncoder(w).Encode`.
pub fn json<T: Serialize + ?Sized>(v: &T) -> Resp {
    let mut r = status(200, format!("{}\n", go_json(v)));
    r.headers_mut().insert(
        header::CONTENT_TYPE,
        HeaderValue::from_static("application/json"),
    );
    r
}

/// A 200 HTML page.
pub fn html(body: String) -> Resp {
    html_bytes(body.into_bytes())
}

/// A 200 HTML page from bytes.
pub fn html_bytes(body: Vec<u8>) -> Resp {
    let mut r = status(200, body);
    r.headers_mut().insert(
        header::CONTENT_TYPE,
        HeaderValue::from_static("text/html; charset=utf-8"),
    );
    r
}

// --- cookies ---

/// net/http's `validCookieValueByte`.
fn valid_cookie_value_byte(b: u8) -> bool {
    (0x20..0x7f).contains(&b) && b != b'"' && b != b';' && b != b'\\'
}

/// net/http's token characters (cookie names).
fn is_token_byte(b: u8) -> bool {
    b.is_ascii_alphanumeric() || b"!#$%&'*+-.^_`|~".contains(&b)
}

/// The `Set-Cookie` line `http.SetCookie` writes for the ui's cookies, which are all
/// HttpOnly, Secure and SameSite=Lax. `max_age` follows Go: positive is written, negative
/// is `Max-Age=0`, zero is omitted. Invalid value and path bytes are dropped, as Go does.
pub fn set_cookie_line(name: &str, value: &str, path: &str, max_age: i64) -> String {
    let mut v: String = value
        .bytes()
        .filter(|b| valid_cookie_value_byte(*b))
        .map(char::from)
        .collect();
    if v.contains([' ', ',']) {
        v = format!("\"{v}\"");
    }
    let mut line = format!("{name}={v}");
    let p: String = path
        .bytes()
        .filter(|b| (0x20..0x7f).contains(b) && *b != b';')
        .map(char::from)
        .collect();
    if !p.is_empty() {
        line.push_str(&format!("; Path={p}"));
    }
    if max_age > 0 {
        line.push_str(&format!("; Max-Age={max_age}"));
    } else if max_age < 0 {
        line.push_str("; Max-Age=0");
    }
    line.push_str("; HttpOnly; Secure; SameSite=Lax");
    line
}

/// Appends a `Set-Cookie` header (see [`set_cookie_line`]).
pub fn add_cookie(h: &mut HeaderMap, name: &str, value: &str, path: &str, max_age: i64) {
    if let Ok(v) = HeaderValue::from_str(&set_cookie_line(name, value, path, max_age)) {
        h.append(header::SET_COOKIE, v);
    }
}

/// Go's `r.Cookie(name)`: the first well-formed cookie of that name across every `Cookie`
/// header, surrounding double quotes stripped. A value with an invalid byte is skipped.
pub fn cookie(h: &HeaderMap, name: &str) -> Option<String> {
    let trim = |s: &[u8]| -> Vec<u8> {
        let is_space = |b: &u8| matches!(b, b' ' | b'\t' | b'\n' | b'\r');
        let start = s.iter().position(|b| !is_space(b)).unwrap_or(s.len());
        let end = s
            .iter()
            .rposition(|b| !is_space(b))
            .map_or(start, |e| e + 1);
        s[start..end].to_vec()
    };
    for line in h.get_all(header::COOKIE) {
        for part in line.as_bytes().split(|b| *b == b';') {
            let part = trim(part);
            if part.is_empty() {
                continue;
            }
            let (n, v) = match part.iter().position(|b| *b == b'=') {
                Some(i) => (&part[..i], &part[i + 1..]),
                None => (&part[..], &[][..]),
            };
            let n = trim(n);
            if n.is_empty() || !n.iter().all(|b| is_token_byte(*b)) || n != name.as_bytes() {
                continue;
            }
            let v = if v.len() > 1 && v[0] == b'"' && v[v.len() - 1] == b'"' {
                &v[1..v.len() - 1]
            } else {
                v
            };
            if !v.iter().all(|b| valid_cookie_value_byte(*b)) {
                continue;
            }
            return Some(String::from_utf8_lossy(v).into_owned());
        }
    }
    None
}

// --- URLs ---

/// The `net/url` escaping modes the ui uses.
#[derive(Clone, Copy, PartialEq, Eq)]
pub enum Mode {
    /// A whole path (`URL.String`).
    Path,
    /// One path segment (`url.PathEscape`, `url.PathUnescape`).
    PathSegment,
    /// A query key or value (`url.QueryEscape`, `url.QueryUnescape`).
    QueryComponent,
}

/// net/url's `shouldEscape`.
fn should_escape(c: u8, mode: Mode) -> bool {
    if c.is_ascii_alphanumeric() || b"-_.~".contains(&c) {
        return false;
    }
    if b"$&+,/:;=?@".contains(&c) {
        return match mode {
            Mode::Path => c == b'?',
            Mode::PathSegment => matches!(c, b'/' | b';' | b',' | b'?'),
            Mode::QueryComponent => true,
        };
    }
    true
}

/// net/url's `escape`.
pub fn escape(s: &[u8], mode: Mode) -> String {
    let mut out = String::with_capacity(s.len());
    for &b in s {
        if b == b' ' && mode == Mode::QueryComponent {
            out.push('+');
        } else if should_escape(b, mode) {
            out.push_str(&format!("%{b:02X}"));
        } else {
            out.push(char::from(b));
        }
    }
    out
}

fn unhex(c: u8) -> Option<u8> {
    match c {
        b'0'..=b'9' => Some(c - b'0'),
        b'a'..=b'f' => Some(c - b'a' + 10),
        b'A'..=b'F' => Some(c - b'A' + 10),
        _ => None,
    }
}

/// net/url's `unescape`; the error is Go's `EscapeError` text.
pub fn unescape(s: &[u8], mode: Mode) -> Result<Vec<u8>, String> {
    let mut out = Vec::with_capacity(s.len());
    let mut i = 0;
    while i < s.len() {
        match s[i] {
            b'%' => {
                let hi = s.get(i + 1).copied().and_then(unhex);
                let lo = s.get(i + 2).copied().and_then(unhex);
                let (Some(hi), Some(lo)) = (hi, lo) else {
                    let bad = &s[i..s.len().min(i + 3)];
                    return Err(format!("invalid URL escape {}", go_quote(bad)));
                };
                out.push(hi << 4 | lo);
                i += 3;
            }
            b'+' if mode == Mode::QueryComponent => {
                out.push(b' ');
                i += 1;
            }
            c => {
                out.push(c);
                i += 1;
            }
        }
    }
    Ok(out)
}

/// net/http's routing `pathUnescape`: the input unchanged when it does not unescape.
pub fn lenient_path_unescape(s: &[u8]) -> Vec<u8> {
    unescape(s, Mode::PathSegment).unwrap_or_else(|_| s.to_vec())
}

/// Go's `url.ParseQuery`, in order: pairs holding a `;` or a bad escape are dropped.
pub fn parse_query(raw: &str) -> Vec<(Vec<u8>, Vec<u8>)> {
    let mut out = Vec::new();
    for part in raw.as_bytes().split(|b| *b == b'&') {
        if part.is_empty() || part.contains(&b';') {
            continue;
        }
        let (k, v) = match part.iter().position(|b| *b == b'=') {
            Some(i) => (&part[..i], &part[i + 1..]),
            None => (part, &[][..]),
        };
        let (Ok(k), Ok(v)) = (
            unescape(k, Mode::QueryComponent),
            unescape(v, Mode::QueryComponent),
        ) else {
            continue;
        };
        out.push((k, v));
    }
    out
}

/// Every value of `key`, in order (`url.Values[key]`).
pub fn query_values(raw: &str, key: &str) -> Vec<Vec<u8>> {
    parse_query(raw)
        .into_iter()
        .filter(|(k, _)| k == key.as_bytes())
        .map(|(_, v)| v)
        .collect()
}

/// `r.URL.Query().Get(key)`: the first value, or empty.
pub fn query_get(raw: &str, key: &str) -> Vec<u8> {
    query_values(raw, key)
        .into_iter()
        .next()
        .unwrap_or_default()
}

/// [`query_get`] as text (invalid UTF-8 replaced).
pub fn query_get_str(raw: &str, key: &str) -> String {
    String::from_utf8_lossy(&query_get(raw, key)).into_owned()
}

/// Go's `path.Clean`.
pub fn path_clean(p: &str) -> String {
    if p.is_empty() {
        return ".".into();
    }
    let rooted = p.starts_with('/');
    let mut parts: Vec<&str> = Vec::new();
    for seg in p.split('/') {
        match seg {
            "" | "." => {}
            ".." => {
                if parts.last().is_some_and(|l| *l != "..") {
                    parts.pop();
                } else if !rooted {
                    parts.push("..");
                }
            }
            s => parts.push(s),
        }
    }
    let joined = parts.join("/");
    match (rooted, joined.is_empty()) {
        (true, _) => format!("/{joined}"),
        (false, true) => ".".into(),
        (false, false) => joined,
    }
}

/// net/http's `cleanPath`: [`path_clean`] keeping a trailing slash.
pub fn clean_path(p: &str) -> String {
    if p.is_empty() {
        return "/".into();
    }
    let p = if p.starts_with('/') {
        p.to_string()
    } else {
        format!("/{p}")
    };
    let mut np = path_clean(&p);
    if p.ends_with('/') && np != "/" {
        np.push('/');
    }
    np
}

/// Go's `path.Base`.
pub fn path_base(p: &str) -> String {
    if p.is_empty() {
        return ".".into();
    }
    let t = p.trim_end_matches('/');
    if t.is_empty() {
        return "/".into();
    }
    t.rsplit('/').next().unwrap_or(t).to_string()
}

// --- strconv ---

/// Whether Go's `strconv.IsPrint` holds, approximated without Unicode tables: control,
/// separator and format characters are not printable.
fn is_print(c: char) -> bool {
    if c == ' ' {
        return true;
    }
    !(c.is_control()
        || c.is_whitespace()
        || matches!(c, '\u{ad}' | '\u{200b}'..='\u{200f}' | '\u{202a}'..='\u{202e}' | '\u{2060}'..='\u{2064}' | '\u{feff}' | '\u{fff9}'..='\u{fffb}'))
}

/// Go's `strconv.Quote` (what `%q` prints for a string).
pub fn go_quote(b: &[u8]) -> String {
    let mut out = String::from("\"");
    let mut rest = b;
    while !rest.is_empty() {
        let (c, n) = match std::str::from_utf8(rest) {
            Ok(s) => s
                .chars()
                .next()
                .map_or((None, 1), |c| (Some(c), c.len_utf8())),
            Err(e) if e.valid_up_to() > 0 => {
                let s = std::str::from_utf8(&rest[..e.valid_up_to()]).unwrap_or_default();
                s.chars()
                    .next()
                    .map_or((None, 1), |c| (Some(c), c.len_utf8()))
            }
            Err(_) => (None, 1),
        };
        match c {
            None => out.push_str(&format!("\\x{:02x}", rest[0])),
            Some('"') => out.push_str("\\\""),
            Some('\\') => out.push_str("\\\\"),
            Some(c) if is_print(c) => out.push(c),
            Some('\u{7}') => out.push_str("\\a"),
            Some('\u{8}') => out.push_str("\\b"),
            Some('\u{c}') => out.push_str("\\f"),
            Some('\n') => out.push_str("\\n"),
            Some('\r') => out.push_str("\\r"),
            Some('\t') => out.push_str("\\t"),
            Some('\u{b}') => out.push_str("\\v"),
            Some(c) if (c as u32) < 0x20 || c == '\u{7f}' => {
                out.push_str(&format!("\\x{:02x}", c as u32));
            }
            Some(c) if (c as u32) < 0x10000 => out.push_str(&format!("\\u{:04x}", c as u32)),
            Some(c) => out.push_str(&format!("\\U{:08x}", c as u32)),
        }
        rest = &rest[n..];
    }
    out.push('"');
    out
}

/// Go's `strconv.ParseInt(s, 10, 64)` as the oapi-codegen binders report it (their
/// `BindStringToObject` wraps the strconv error).
pub fn parse_int(b: &[u8]) -> Result<i64, String> {
    let err = |why: &str| {
        format!(
            "error binding string parameter: strconv.ParseInt: parsing {}: {why}",
            go_quote(b)
        )
    };
    let Ok(s) = std::str::from_utf8(b) else {
        return Err(err("invalid syntax"));
    };
    s.parse::<i64>().map_err(|e| match e.kind() {
        std::num::IntErrorKind::PosOverflow | std::num::IntErrorKind::NegOverflow => {
            err("value out of range")
        }
        _ => err("invalid syntax"),
    })
}

// --- content sniffing ---

/// Go's `http.DetectContentType`, reduced to the signatures a tier response or an SPA
/// asset can plausibly carry: HTML and XML markers, PDF, the image and font formats, gzip,
/// zip and wasm; otherwise text when no binary byte occurs in the first 512 bytes.
pub fn sniff(data: &[u8]) -> &'static str {
    let data = &data[..data.len().min(512)];
    let ws = |b: &u8| matches!(b, b'\t' | b'\n' | b'\x0c' | b'\r' | b' ');
    let start = data.iter().position(|b| !ws(b)).unwrap_or(data.len());
    let lead = &data[start..];
    let html_tags: [&[u8]; 17] = [
        b"<!DOCTYPE HTML",
        b"<HTML",
        b"<HEAD",
        b"<SCRIPT",
        b"<IFRAME",
        b"<H1",
        b"<DIV",
        b"<FONT",
        b"<TABLE",
        b"<A",
        b"<STYLE",
        b"<TITLE",
        b"<B",
        b"<BODY",
        b"<BR",
        b"<P",
        b"<!--",
    ];
    for tag in html_tags {
        if lead.len() > tag.len()
            && lead[..tag.len()].eq_ignore_ascii_case(tag)
            && matches!(lead[tag.len()], b' ' | b'>')
        {
            return "text/html; charset=utf-8";
        }
    }
    if lead.starts_with(b"<?xml") {
        return "text/xml; charset=utf-8";
    }
    let magic: [(&[u8], &'static str); 12] = [
        (b"%PDF-", "application/pdf"),
        (b"\x89PNG\x0D\x0A\x1A\x0A", "image/png"),
        (b"GIF87a", "image/gif"),
        (b"GIF89a", "image/gif"),
        (b"\xFF\xD8\xFF", "image/jpeg"),
        (b"\x00\x00\x01\x00", "image/x-icon"),
        (b"wOFF", "font/woff"),
        (b"wOF2", "font/woff2"),
        (b"\x1F\x8B\x08", "application/x-gzip"),
        (b"PK\x03\x04", "application/zip"),
        (b"\x00\x61\x73\x6D", "application/wasm"),
        (b"BM", "image/bmp"),
    ];
    for (m, ct) in magic {
        if data.starts_with(m) {
            return ct;
        }
    }
    if data.len() >= 12 && &data[..4] == b"RIFF" && &data[8..12] == b"WEBP" {
        return "image/webp";
    }
    let binary = |b: &u8| matches!(b, 0x00..=0x08 | 0x0b | 0x0e..=0x1a | 0x1c..=0x1f);
    if data.iter().any(binary) {
        "application/octet-stream"
    } else {
        "text/plain; charset=utf-8"
    }
}

#[cfg(test)]
mod tests;
