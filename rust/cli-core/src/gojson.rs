//! JSON the way Go's `encoding/json` writes it: HTML-safe string escapes, Go's
//! float format, `MarshalIndent` layout. Objects keep the order they are built
//! in; build a Go map's keys sorted, as Go does.

use crate::gofmt::{format_e, format_f};

/// A JSON value to encode.
#[derive(Clone, Debug, PartialEq)]
pub enum Value {
    /// `null` (a nil slice, map or pointer in Go).
    Null,
    /// `true` / `false`.
    Bool(bool),
    /// An integer.
    Int(i64),
    /// An unsigned integer that may not fit an `i64`.
    Uint(u64),
    /// A float64, formatted as Go does. NaN and infinities are not valid JSON.
    Float(f64),
    /// A string.
    Str(String),
    /// An array.
    Array(Vec<Value>),
    /// An object, in field order.
    Object(Vec<(String, Value)>),
}

impl Value {
    /// A string value.
    pub fn str(s: impl Into<String>) -> Self {
        Value::Str(s.into())
    }

    /// An array of strings (`[]string`, never nil).
    pub fn strings<S: AsRef<str>>(items: &[S]) -> Self {
        Value::Array(items.iter().map(|s| Value::str(s.as_ref())).collect())
    }
}

/// `json.Marshal`.
pub fn marshal(v: &Value) -> Result<String, String> {
    let mut out = String::new();
    write(&mut out, v, None, 0)?;
    Ok(out)
}

/// `json.MarshalIndent(v, "", indent)`.
pub fn marshal_indent(v: &Value, indent: &str) -> Result<String, String> {
    let mut out = String::new();
    write(&mut out, v, Some(indent), 0)?;
    Ok(out)
}

fn newline(out: &mut String, indent: Option<&str>, depth: usize) {
    if let Some(ind) = indent {
        out.push('\n');
        for _ in 0..depth {
            out.push_str(ind);
        }
    }
}

fn write(out: &mut String, v: &Value, indent: Option<&str>, depth: usize) -> Result<(), String> {
    match v {
        Value::Null => out.push_str("null"),
        Value::Bool(b) => out.push_str(if *b { "true" } else { "false" }),
        Value::Int(i) => out.push_str(&i.to_string()),
        Value::Uint(u) => out.push_str(&u.to_string()),
        Value::Float(f) => out.push_str(&float(*f)?),
        Value::Str(s) => string(out, s),
        Value::Array(items) => {
            if items.is_empty() {
                out.push_str("[]");
                return Ok(());
            }
            out.push('[');
            for (i, item) in items.iter().enumerate() {
                if i > 0 {
                    out.push(',');
                }
                newline(out, indent, depth + 1);
                write(out, item, indent, depth + 1)?;
            }
            newline(out, indent, depth);
            out.push(']');
        }
        Value::Object(fields) => {
            if fields.is_empty() {
                out.push_str("{}");
                return Ok(());
            }
            out.push('{');
            for (i, (k, item)) in fields.iter().enumerate() {
                if i > 0 {
                    out.push(',');
                }
                newline(out, indent, depth + 1);
                string(out, k);
                out.push(':');
                if indent.is_some() {
                    out.push(' ');
                }
                write(out, item, indent, depth + 1)?;
            }
            newline(out, indent, depth);
            out.push('}');
        }
    }
    Ok(())
}

/// Go's float64 encoder: `'f'` between 1e-6 and 1e21, `'e'` outside, with a
/// two-digit negative exponent trimmed to one ("1e-07" becomes "1e-7").
fn float(f: f64) -> Result<String, String> {
    if !f.is_finite() {
        return Err(format!(
            "json: unsupported value: {}",
            crate::gofmt::format_v(f)
        ));
    }
    let abs = f.abs();
    if abs != 0.0 && !(1e-6..1e21).contains(&abs) {
        let mut s = format_e(f);
        let b = s.as_bytes();
        let n = b.len();
        if n >= 4 && b[n - 4] == b'e' && b[n - 3] == b'-' && b[n - 2] == b'0' {
            s.remove(n - 2);
        }
        Ok(s)
    } else {
        Ok(format_f(f))
    }
}

/// A JSON string with Go's escapes: `<`, `>`, `&`, U+2028 and U+2029 as `\u` codes.
fn string(out: &mut String, s: &str) {
    out.push('"');
    for c in s.chars() {
        match c {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            '\u{8}' => out.push_str("\\b"),
            '\u{c}' => out.push_str("\\f"),
            '<' | '>' | '&' | '\u{2028}' | '\u{2029}' => {
                out.push_str(&format!("\\u{:04x}", c as u32));
            }
            c if (c as u32) < 0x20 => out.push_str(&format!("\\u{:04x}", c as u32)),
            c => out.push(c),
        }
    }
    out.push('"');
}

#[cfg(test)]
mod tests;
