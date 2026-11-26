//! Go `encoding/json` compatibility for the wire: the encoder's bytes (HTML-safe
//! escaping, trailing newline) and the decoder's behaviour and error text.
//!
//! `json.NewDecoder(r.Body).Decode(&v)` reads only the first JSON value (trailing
//! bytes are ignored), matches object keys case-insensitively, treats `null` as "leave
//! the zero value", records the first type mismatch while decoding the rest, and stops
//! at the first error a custom `UnmarshalJSON` returns. Handlers answer a failed decode
//! with the error's text, so [`decode`] reproduces those messages.

use serde::Serialize;
use serde::de::DeserializeOwned;
use serde_json::Value;

mod scanner;
pub mod shape;

pub use shape::Shape;

/// Encodes `v` as Go's `json.NewEncoder(w).Encode(v)` does: `<`, `>`, `&`, U+2028
/// and U+2029 escaped inside strings, and a trailing newline.
pub fn encode<T: Serialize + ?Sized>(v: &T) -> Vec<u8> {
    let mut out = Vec::with_capacity(128);
    let mut ser = serde_json::Serializer::with_formatter(&mut out, GoFormatter);
    if v.serialize(&mut ser).is_err() {
        // Every type we encode is a plain struct, map or sequence of strings and
        // numbers; serde_json cannot fail on those.
        out.clear();
        out.extend_from_slice(b"null");
    }
    out.push(b'\n');
    out
}

/// serde_json's compact formatter with Go's HTML-safe string escaping.
struct GoFormatter;

impl serde_json::ser::Formatter for GoFormatter {
    fn write_string_fragment<W: ?Sized + std::io::Write>(
        &mut self,
        writer: &mut W,
        fragment: &str,
    ) -> std::io::Result<()> {
        let mut start = 0;
        for (i, ch) in fragment.char_indices() {
            let esc = match ch {
                '<' => "\\u003c",
                '>' => "\\u003e",
                '&' => "\\u0026",
                '\u{2028}' => "\\u2028",
                '\u{2029}' => "\\u2029",
                _ => continue,
            };
            writer.write_all(&fragment.as_bytes()[start..i])?;
            writer.write_all(esc.as_bytes())?;
            start = i + ch.len_utf8();
        }
        writer.write_all(&fragment.as_bytes()[start..])
    }
}

/// Go's `json.Compact` with HTML escaping, as the encoder applies it to a
/// `json.RawMessage`: insignificant whitespace dropped, key order and number text
/// kept, `<`, `>`, `&`, U+2028 and U+2029 escaped inside strings.
pub fn compact(raw: &[u8]) -> String {
    let text = String::from_utf8_lossy(raw);
    let mut out = String::with_capacity(text.len());
    let mut in_string = false;
    let mut escaped = false;
    for ch in text.chars() {
        if in_string {
            match ch {
                _ if escaped => {
                    escaped = false;
                    out.push(ch);
                }
                '\\' => {
                    escaped = true;
                    out.push(ch);
                }
                '"' => {
                    in_string = false;
                    out.push(ch);
                }
                '<' => out.push_str("\\u003c"),
                '>' => out.push_str("\\u003e"),
                '&' => out.push_str("\\u0026"),
                '\u{2028}' => out.push_str("\\u2028"),
                '\u{2029}' => out.push_str("\\u2029"),
                _ => out.push(ch),
            }
            continue;
        }
        match ch {
            ' ' | '\t' | '\n' | '\r' => {}
            '"' => {
                in_string = true;
                out.push(ch);
            }
            _ => out.push(ch),
        }
    }
    out
}

/// Decodes the first JSON value of `body` into `T` the way Go's `json.Decoder`
/// decodes into the Go type `shape` describes. `Err` carries Go's error text.
pub fn decode<T: DeserializeOwned>(body: &[u8], shape: &Shape) -> Result<T, String> {
    let value = first_value(body)?;
    let value: Value = serde_json::from_slice(value).map_err(|e| e.to_string())?;
    let canonical = shape::canonicalize(&value, shape)?;
    serde_json::from_value(canonical).map_err(|e| e.to_string())
}

/// The bytes of the first JSON value in `body`, or Go's decoder error: `EOF` for an
/// empty body, `unexpected EOF` for a truncated value, the scanner's syntax error
/// otherwise.
pub fn first_value(body: &[u8]) -> Result<&[u8], String> {
    let start = body
        .iter()
        .position(|c| !matches!(c, b' ' | b'\t' | b'\r' | b'\n'))
        .ok_or_else(|| "EOF".to_string())?;
    let data = &body[start..];
    let mut s = scanner::Scanner::new();
    for (i, &c) in data.iter().enumerate() {
        let res = s.step(c);
        if s.end_top {
            // A scalar ends at the delimiter after it; anything else at its last byte.
            return Ok(if s.ended_on_delimiter {
                &data[..i]
            } else {
                &data[..=i]
            });
        }
        res?;
    }
    let _ = s.step(b' ');
    if s.end_top {
        return Ok(data);
    }
    Err("unexpected EOF".to_string())
}

#[cfg(test)]
mod tests;
