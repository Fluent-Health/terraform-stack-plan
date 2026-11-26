//! JSON the way Go's `encoding/json` writes it: HTML-safe string escapes, Go's
//! float format, `MarshalIndent` layout. Objects keep the order they are built
//! in; build a Go map's keys sorted, as Go does.

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

/// Go's float64 encoder (`tfstackplan_render::gofmt::format_json_float`); NaN and
/// infinities are errors, as in Go.
fn float(f: f64) -> Result<String, String> {
    if !f.is_finite() {
        return Err(format!(
            "json: unsupported value: {}",
            crate::gofmt::format_v(f)
        ));
    }
    Ok(tfstackplan_render::gofmt::format_json_float(f))
}

/// A JSON string with Go's escapes (`tfstackplan_render::goval::json_string`).
fn string(out: &mut String, s: &str) {
    tfstackplan_render::goval::json_string(out, s);
}

#[cfg(test)]
mod tests;
