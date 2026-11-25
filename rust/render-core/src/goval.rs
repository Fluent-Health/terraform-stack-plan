//! A dynamic value shaped like what Go's decoders put in an `any`, and Go's JSON encoding of it.
//!
//! `encoding/json` gives `map[string]any`, `[]any`, `float64`, `string`, `bool` and `nil`.
//! `yaml.v3` adds `int`, `uint64`, `time.Time` and `map[any]any` (a mapping with a non-string
//! key). Keeping the Go type matters: it decides how a value prints and whether Go's JSON
//! encoder accepts it.

use std::collections::BTreeMap;

use crate::gofmt;
use crate::yaml::Time;

/// A decoded value with its Go type.
#[derive(Debug, Clone, PartialEq)]
pub enum Value {
    /// `nil`.
    Null,
    /// `bool`.
    Bool(bool),
    /// `int` (64-bit).
    Int(i64),
    /// `uint64`, for YAML integers above `int64`.
    Uint(u64),
    /// `float64`.
    Float(f64),
    /// `string`.
    Str(String),
    /// `time.Time`, from an unquoted YAML timestamp.
    Time(Time),
    /// `[]any`.
    Seq(Vec<Value>),
    /// `map[string]any`, keys in Go's sorted order.
    Map(BTreeMap<String, Value>),
    /// `map[any]any`: a YAML mapping with at least one non-string key.
    AnyMap(Vec<(Value, Value)>),
}

impl Value {
    /// The string, if this is one.
    pub fn as_str(&self) -> Option<&str> {
        match self {
            Value::Str(s) => Some(s),
            _ => None,
        }
    }

    /// The `map[string]any`, if this is one.
    pub fn as_map(&self) -> Option<&BTreeMap<String, Value>> {
        match self {
            Value::Map(m) => Some(m),
            _ => None,
        }
    }

    /// True for a map or a list (`map[string]any`, `map[any]any` or `[]any`).
    pub fn is_structured(&self) -> bool {
        matches!(self, Value::Map(_) | Value::AnyMap(_) | Value::Seq(_))
    }

    /// Converts a parsed JSON value the way `encoding/json` decodes into `any`.
    pub fn from_json(v: serde_json::Value) -> Value {
        match v {
            serde_json::Value::Null => Value::Null,
            serde_json::Value::Bool(b) => Value::Bool(b),
            serde_json::Value::Number(n) => Value::Float(n.as_f64().unwrap_or(0.0)),
            serde_json::Value::String(s) => Value::Str(s),
            serde_json::Value::Array(a) => Value::Seq(a.into_iter().map(Value::from_json).collect()),
            serde_json::Value::Object(o) => {
                Value::Map(o.into_iter().map(|(k, v)| (k, Value::from_json(v))).collect())
            }
        }
    }
}

/// Parses JSON into a [`Value`], as `json.Unmarshal` into `any` does.
pub fn parse_json(s: &str) -> Option<Value> {
    serde_json::from_str::<serde_json::Value>(s)
        .ok()
        .map(Value::from_json)
}

/// Writes `s` as a JSON string the way `encoding/json` does with HTML escaping on (its default).
pub fn json_string(out: &mut String, s: &str) {
    out.push('"');
    for c in s.chars() {
        match c {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\u{8}' => out.push_str("\\b"),
            '\u{c}' => out.push_str("\\f"),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            '<' | '>' | '&' | '\u{2028}' | '\u{2029}' => {
                out.push_str(&format!("\\u{:04x}", c as u32));
            }
            c if (c as u32) < 0x20 => out.push_str(&format!("\\u{:04x}", c as u32)),
            c => out.push(c),
        }
    }
    out.push('"');
}

/// `json.MarshalIndent(v, "", "  ")`, or `None` where Go returns an error (a `map[any]any`, a
/// NaN or infinite float, a time outside years 0 to 9999).
pub fn marshal_indent(v: &Value) -> Option<String> {
    let mut out = String::new();
    write_json(&mut out, v, 0)?;
    Some(out)
}

fn newline(out: &mut String, depth: usize) {
    out.push('\n');
    for _ in 0..depth {
        out.push_str("  ");
    }
}

fn write_json(out: &mut String, v: &Value, depth: usize) -> Option<()> {
    match v {
        Value::Null => out.push_str("null"),
        Value::Bool(b) => out.push_str(if *b { "true" } else { "false" }),
        Value::Int(i) => out.push_str(&i.to_string()),
        Value::Uint(u) => out.push_str(&u.to_string()),
        Value::Float(f) => {
            if !f.is_finite() {
                return None;
            }
            out.push_str(&gofmt::format_json_float(*f));
        }
        Value::Str(s) => json_string(out, s),
        Value::Time(t) => {
            if !(0..=9999).contains(&t.year) {
                return None;
            }
            json_string(out, &t.rfc3339_nano());
        }
        Value::Seq(items) => {
            if items.is_empty() {
                out.push_str("[]");
                return Some(());
            }
            out.push('[');
            for (i, item) in items.iter().enumerate() {
                if i > 0 {
                    out.push(',');
                }
                newline(out, depth + 1);
                write_json(out, item, depth + 1)?;
            }
            newline(out, depth);
            out.push(']');
        }
        Value::Map(m) => {
            if m.is_empty() {
                out.push_str("{}");
                return Some(());
            }
            out.push('{');
            for (i, (k, item)) in m.iter().enumerate() {
                if i > 0 {
                    out.push(',');
                }
                newline(out, depth + 1);
                json_string(out, k);
                out.push_str(": ");
                write_json(out, item, depth + 1)?;
            }
            newline(out, depth);
            out.push('}');
        }
        Value::AnyMap(_) => return None,
    }
    Some(())
}

#[cfg(test)]
mod tests;
