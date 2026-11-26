//! A decoded YAML value as Go's `yaml.v3` hands it back in a `map[string]any`,
//! and how Go prints it: `fmt.Sprint` (`%v`) and `encoding/json`.

use crate::gofmt::format_v;
use crate::gojson;

/// A `time.Time` parsed from a plain YAML timestamp.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Timestamp {
    /// Year.
    pub year: i32,
    /// Month, 1-12.
    pub month: u32,
    /// Day of month.
    pub day: u32,
    /// Hour.
    pub hour: u32,
    /// Minute.
    pub minute: u32,
    /// Second.
    pub second: u32,
    /// Nanoseconds.
    pub nanos: u32,
    /// Offset east of UTC in seconds; `None` (or zero) is UTC.
    pub offset: Option<i32>,
}

impl Timestamp {
    fn fraction(&self) -> String {
        if self.nanos == 0 {
            return String::new();
        }
        let s = format!("{:09}", self.nanos);
        format!(".{}", s.trim_end_matches('0'))
    }

    fn utc(&self) -> bool {
        self.offset.is_none_or(|o| o == 0)
    }

    /// `t.String()`: `2006-01-02 15:04:05.999999999 -0700 MST`.
    pub fn go_string(&self) -> String {
        let base = format!(
            "{:04}-{:02}-{:02} {:02}:{:02}:{:02}{}",
            self.year,
            self.month,
            self.day,
            self.hour,
            self.minute,
            self.second,
            self.fraction()
        );
        if self.utc() {
            return format!("{base} +0000 UTC");
        }
        let off = self.offset.unwrap_or(0);
        let sign = if off < 0 { '-' } else { '+' };
        let a = off.unsigned_abs();
        let z = format!("{sign}{:02}{:02}", a / 3600, (a % 3600) / 60);
        // A fabricated zone has no name, so Go prints the offset twice.
        format!("{base} {z} {z}")
    }

    /// `t.MarshalJSON()`: RFC 3339 with nanoseconds.
    pub fn rfc3339(&self) -> String {
        let base = format!(
            "{:04}-{:02}-{:02}T{:02}:{:02}:{:02}{}",
            self.year,
            self.month,
            self.day,
            self.hour,
            self.minute,
            self.second,
            self.fraction()
        );
        if self.utc() {
            return format!("{base}Z");
        }
        let off = self.offset.unwrap_or(0);
        let sign = if off < 0 { '-' } else { '+' };
        let a = off.unsigned_abs();
        format!("{base}{sign}{:02}:{:02}", a / 3600, (a % 3600) / 60)
    }
}

/// A flattened (or raw) input value.
#[derive(Clone, Debug, PartialEq)]
pub enum Value {
    /// `nil`.
    Nil,
    /// `bool`.
    Bool(bool),
    /// `int`.
    Int(i64),
    /// `uint64` (an integer above `int64`).
    Uint(u64),
    /// `float64`.
    Float(f64),
    /// `string`.
    Str(String),
    /// `time.Time` (a plain YAML timestamp).
    Time(Timestamp),
    /// `[]any`.
    Seq(Vec<Value>),
    /// `map[string]any`, sorted by key.
    Map(Vec<(String, Value)>),
    /// `map[any]any` (a YAML mapping with a non-string key), in source order.
    AnyMap(Vec<(Value, Value)>),
    /// The `List` sentinel `Flatten` emits for a list: its elements stringified.
    List(Vec<String>),
}

impl Value {
    /// A string value.
    pub fn str(s: &str) -> Self {
        Value::Str(s.to_string())
    }

    /// A `List` sentinel.
    pub fn list(elems: &[&str]) -> Self {
        Value::List(elems.iter().map(|s| s.to_string()).collect())
    }

    /// `fmt.Sprint(v)`.
    pub fn sprint(&self) -> String {
        match self {
            Value::Nil => "<nil>".to_string(),
            Value::Bool(b) => b.to_string(),
            Value::Int(i) => i.to_string(),
            Value::Uint(u) => u.to_string(),
            Value::Float(f) => format_v(*f),
            Value::Str(s) => s.clone(),
            Value::Time(t) => t.go_string(),
            Value::Seq(items) => format!(
                "[{}]",
                items
                    .iter()
                    .map(Value::sprint)
                    .collect::<Vec<_>>()
                    .join(" ")
            ),
            Value::Map(entries) => format!(
                "map[{}]",
                entries
                    .iter()
                    .map(|(k, v)| format!("{k}:{}", v.sprint()))
                    .collect::<Vec<_>>()
                    .join(" ")
            ),
            Value::AnyMap(entries) => {
                // fmt sorts map keys: numbers numerically, strings lexically,
                // and different types by kind. This orders within a kind.
                let mut sorted: Vec<&(Value, Value)> = entries.iter().collect();
                sorted.sort_by(|a, b| key_order(&a.0, &b.0));
                format!(
                    "map[{}]",
                    sorted
                        .iter()
                        .map(|(k, v)| format!("{}:{}", k.sprint(), v.sprint()))
                        .collect::<Vec<_>>()
                        .join(" ")
                )
            }
            Value::List(elems) => format!("{{[{}]}}", elems.join(" ")),
        }
    }

    /// The Go `%T` of the value, for error messages.
    pub fn go_type(&self) -> &'static str {
        match self {
            Value::Nil => "<nil>",
            Value::Bool(_) => "bool",
            Value::Int(_) => "int",
            Value::Uint(_) => "uint64",
            Value::Float(_) => "float64",
            Value::Str(_) => "string",
            Value::Time(_) => "time.Time",
            Value::Seq(_) => "[]interface {}",
            Value::Map(_) => "map[string]interface {}",
            Value::AnyMap(_) => "map[interface {}]interface {}",
            Value::List(_) => "uniqueness.List",
        }
    }

    /// The value as `encoding/json` marshals it.
    pub fn to_json(&self) -> Result<gojson::Value, String> {
        Ok(match self {
            Value::Nil => gojson::Value::Null,
            Value::Bool(b) => gojson::Value::Bool(*b),
            Value::Int(i) => gojson::Value::Int(*i),
            Value::Uint(u) => gojson::Value::Uint(*u),
            Value::Float(f) => {
                if !f.is_finite() {
                    return Err(format!("json: unsupported value: {}", format_v(*f)));
                }
                gojson::Value::Float(*f)
            }
            Value::Str(s) => gojson::Value::Str(s.clone()),
            Value::Time(t) => gojson::Value::Str(t.rfc3339()),
            Value::Seq(items) => gojson::Value::Array(
                items
                    .iter()
                    .map(Value::to_json)
                    .collect::<Result<Vec<_>, _>>()?,
            ),
            Value::Map(entries) => gojson::Value::Object(
                entries
                    .iter()
                    .map(|(k, v)| Ok((k.clone(), v.to_json()?)))
                    .collect::<Result<Vec<_>, String>>()?,
            ),
            Value::AnyMap(_) => {
                return Err("json: unsupported type: map[interface {}]interface {}".to_string());
            }
            Value::List(elems) => {
                gojson::Value::Object(vec![("Elems".to_string(), gojson::Value::strings(elems))])
            }
        })
    }
}

fn kind_rank(v: &Value) -> u8 {
    match v {
        Value::Nil => 0,
        Value::Bool(_) => 1,
        Value::Int(_) => 2,
        Value::Uint(_) => 3,
        Value::Float(_) => 4,
        Value::Str(_) => 5,
        _ => 6,
    }
}

fn key_order(a: &Value, b: &Value) -> std::cmp::Ordering {
    match (a, b) {
        (Value::Int(x), Value::Int(y)) => x.cmp(y),
        (Value::Uint(x), Value::Uint(y)) => x.cmp(y),
        (Value::Float(x), Value::Float(y)) => x.total_cmp(y),
        (Value::Str(x), Value::Str(y)) => x.cmp(y),
        (Value::Bool(x), Value::Bool(y)) => x.cmp(y),
        _ => kind_rank(a)
            .cmp(&kind_rank(b))
            .then_with(|| a.sprint().cmp(&b.sprint())),
    }
}

#[cfg(test)]
mod tests;
