//! `yaml.Unmarshal(data, &map[string]any{})` as Go's `yaml.v3` does it, on
//! `yaml-rust2`'s event parser. Plain scalars are resolved with yaml.v3's rules
//! (YAML 1.2 core schema plus 1.1 octals, `0b`, underscores, timestamps as
//! `time.Time`); quoted and block scalars stay strings.
//!
//! Syntax errors carry `yaml-rust2`'s wording, not yaml.v3's. Merge keys (`<<`)
//! are kept as ordinary keys rather than merged.

use std::collections::BTreeMap;
use std::sync::LazyLock;

use regex::Regex;
use yaml_rust2::parser::{Event, MarkedEventReceiver, Parser, Tag};
use yaml_rust2::scanner::{Marker, TScalarStyle};

use super::value::{Timestamp, Value};

#[derive(Clone, Debug)]
enum Node {
    Scalar {
        value: String,
        style: TScalarStyle,
        tag: Option<Tag>,
        line: usize,
    },
    Seq(Vec<Node>, usize),
    Map(Vec<(Node, Node)>, usize),
}

enum Frame {
    Seq(Vec<Node>, usize, usize),
    Map(Vec<Node>, usize, usize),
}

#[derive(Default)]
struct Builder {
    stack: Vec<Frame>,
    anchors: BTreeMap<usize, Node>,
    root: Option<Node>,
}

impl Builder {
    fn push(&mut self, node: Node, anchor: usize) {
        if anchor > 0 {
            self.anchors.insert(anchor, node.clone());
        }
        match self.stack.last_mut() {
            Some(Frame::Seq(items, _, _)) | Some(Frame::Map(items, _, _)) => items.push(node),
            None => {
                if self.root.is_none() {
                    self.root = Some(node);
                }
            }
        }
    }
}

impl MarkedEventReceiver for Builder {
    fn on_event(&mut self, ev: Event, mark: Marker) {
        match ev {
            Event::Scalar(value, style, anchor, tag) => self.push(
                Node::Scalar {
                    value,
                    style,
                    tag,
                    line: mark.line(),
                },
                anchor,
            ),
            Event::SequenceStart(anchor, _) => {
                self.stack.push(Frame::Seq(Vec::new(), anchor, mark.line()));
            }
            Event::MappingStart(anchor, _) => {
                self.stack.push(Frame::Map(Vec::new(), anchor, mark.line()));
            }
            Event::SequenceEnd | Event::MappingEnd => {
                let (node, anchor) = match self.stack.pop() {
                    Some(Frame::Seq(items, a, line)) => (Node::Seq(items, line), a),
                    Some(Frame::Map(items, a, line)) => {
                        let mut pairs = Vec::new();
                        let mut it = items.into_iter();
                        while let (Some(k), Some(v)) = (it.next(), it.next()) {
                            pairs.push((k, v));
                        }
                        (Node::Map(pairs, line), a)
                    }
                    None => return,
                };
                self.push(node, anchor);
            }
            Event::Alias(id) => {
                let node = self.anchors.get(&id).cloned().unwrap_or(Node::Scalar {
                    value: String::new(),
                    style: TScalarStyle::Plain,
                    tag: None,
                    line: mark.line(),
                });
                self.push(node, 0);
            }
            _ => {}
        }
    }
}

static YAML_STYLE_FLOAT: LazyLock<Regex> = LazyLock::new(|| {
    #[expect(
        clippy::expect_used,
        reason = "a constant pattern; covered by the tests"
    )]
    Regex::new(r"^[-+]?(\.[0-9]+|[0-9]+(\.[0-9]*)?)([eE][-+]?[0-9]+)?$")
        .expect("valid float pattern")
});

fn is_str_tag(tag: &Option<Tag>) -> bool {
    tag.as_ref().is_some_and(|t| {
        (t.handle == "!!" || t.handle == "tag:yaml.org,2002:") && t.suffix == "str"
    })
}

fn indicated_string(style: TScalarStyle, tag: &Option<Tag>) -> bool {
    style != TScalarStyle::Plain || is_str_tag(tag)
}

/// Go's `strconv.ParseInt(s, 0, 64)` / `ParseUint`, enough for yaml.v3's resolver.
fn parse_go_int(s: &str) -> Option<Value> {
    let (neg, body) = match s.as_bytes().first() {
        Some(b'-') => (true, &s[1..]),
        Some(b'+') => (false, &s[1..]),
        _ => (false, s),
    };
    if body.is_empty() {
        return None;
    }
    let (radix, digits) = if let Some(d) = body.strip_prefix("0x").or(body.strip_prefix("0X")) {
        (16, d)
    } else if let Some(d) = body.strip_prefix("0o").or(body.strip_prefix("0O")) {
        (8, d)
    } else if let Some(d) = body.strip_prefix("0b").or(body.strip_prefix("0B")) {
        (2, d)
    } else if body.len() > 1 && body.starts_with('0') {
        (8, &body[1..])
    } else {
        (10, body)
    };
    if digits.is_empty() || !digits.chars().all(|c| c.is_digit(radix)) {
        return None;
    }
    let mag = u128::from_str_radix(digits, radix).ok()?;
    if neg {
        let v = i128::try_from(mag).ok()?;
        i64::try_from(-v).ok().map(Value::Int)
    } else if let Ok(v) = i64::try_from(mag) {
        Some(Value::Int(v))
    } else {
        u64::try_from(mag).ok().map(Value::Uint)
    }
}

fn digits_1_2(s: &str) -> Option<(u32, &str)> {
    let n = s.bytes().take(2).take_while(u8::is_ascii_digit).count();
    if n == 0 {
        return None;
    }
    Some((s[..n].parse().ok()?, &s[n..]))
}

fn days_in_month(y: i32, m: u32) -> u32 {
    match m {
        2 if (y % 4 == 0 && y % 100 != 0) || y % 400 == 0 => 29,
        2 => 28,
        4 | 6 | 9 | 11 => 30,
        _ => 31,
    }
}

/// yaml.v3's `parseTimestamp`: `time.Parse` against its four layouts.
pub(crate) fn parse_timestamp(s: &str) -> Option<Timestamp> {
    let b = s.as_bytes();
    if b.len() < 5 || !b[..4].iter().all(u8::is_ascii_digit) || b[4] != b'-' {
        return None;
    }
    let year: i32 = s[..4].parse().ok()?;
    let (month, rest) = digits_1_2(&s[5..])?;
    let rest = rest.strip_prefix('-')?;
    let (day, rest) = digits_1_2(rest)?;
    if !(1..=12).contains(&month) || day < 1 || day > days_in_month(year, month) {
        return None;
    }
    let mut ts = Timestamp {
        year,
        month,
        day,
        hour: 0,
        minute: 0,
        second: 0,
        nanos: 0,
        offset: None,
    };
    if rest.is_empty() {
        return Some(ts);
    }
    let sep = rest.chars().next()?;
    let zoned = match sep {
        'T' | 't' => true,
        ' ' => false,
        _ => return None,
    };
    let rest = &rest[1..];
    let (hour, rest) = digits_1_2(rest)?;
    let rest = rest.strip_prefix(':')?;
    let (minute, rest) = digits_1_2(rest)?;
    let rest = rest.strip_prefix(':')?;
    let (second, mut rest) = digits_1_2(rest)?;
    if hour > 23 || minute > 59 || second > 59 {
        return None;
    }
    if let Some(frac) = rest.strip_prefix('.') {
        let n = frac.bytes().take_while(u8::is_ascii_digit).count();
        if n == 0 || n > 9 {
            return None;
        }
        let padded = format!("{:0<9}", &frac[..n]);
        ts.nanos = padded.parse().ok()?;
        rest = &frac[n..];
    }
    ts.hour = hour;
    ts.minute = minute;
    ts.second = second;
    if !zoned {
        return rest.is_empty().then_some(ts);
    }
    if rest == "Z" {
        return Some(ts);
    }
    let rb = rest.as_bytes();
    if rb.len() != 6 || !(rb[0] == b'+' || rb[0] == b'-') || rb[3] != b':' {
        return None;
    }
    let hh: i32 = rest[1..3].parse().ok()?;
    let mm: i32 = rest[4..6].parse().ok()?;
    if hh > 24 || mm > 59 {
        return None;
    }
    let off = (hh * 3600 + mm * 60) * if rb[0] == b'-' { -1 } else { 1 };
    ts.offset = Some(off);
    Some(ts)
}

/// yaml.v3's `resolve` for an untagged plain scalar.
fn resolve_plain(s: &str) -> Value {
    match s {
        "true" | "True" | "TRUE" => return Value::Bool(true),
        "false" | "False" | "FALSE" => return Value::Bool(false),
        "" | "~" | "null" | "Null" | "NULL" => return Value::Nil,
        ".nan" | ".NaN" | ".NAN" => return Value::Float(f64::NAN),
        ".inf" | ".Inf" | ".INF" | "+.inf" | "+.Inf" | "+.INF" => {
            return Value::Float(f64::INFINITY);
        }
        "-.inf" | "-.Inf" | "-.INF" => return Value::Float(f64::NEG_INFINITY),
        _ => {}
    }
    match s.as_bytes()[0] {
        b'.' => {
            if let Ok(f) = s.parse::<f64>() {
                return Value::Float(f);
            }
        }
        b'0'..=b'9' | b'+' | b'-' => {
            if let Some(t) = parse_timestamp(s) {
                return Value::Time(t);
            }
            let plain = s.replace('_', "");
            if let Some(v) = parse_go_int(&plain) {
                return v;
            }
            if YAML_STYLE_FLOAT.is_match(&plain)
                && let Ok(f) = plain.parse::<f64>()
            {
                return Value::Float(f);
            }
        }
        _ => {}
    }
    Value::Str(s.to_string())
}

fn node_line(n: &Node) -> usize {
    match n {
        Node::Scalar { line, .. } | Node::Seq(_, line) | Node::Map(_, line) => *line,
    }
}

/// Decodes a node into `any`.
fn to_any(n: &Node) -> Result<Value, String> {
    match n {
        Node::Scalar {
            value, style, tag, ..
        } => {
            if indicated_string(*style, tag) {
                Ok(Value::Str(value.clone()))
            } else {
                Ok(resolve_plain(value))
            }
        }
        Node::Seq(items, _) => Ok(Value::Seq(
            items.iter().map(to_any).collect::<Result<Vec<_>, _>>()?,
        )),
        Node::Map(pairs, _) => {
            check_unique_keys(pairs)?;
            let mut keys = Vec::new();
            for (k, v) in pairs {
                keys.push((to_any(k)?, to_any(v)?));
            }
            if keys.iter().all(|(k, _)| matches!(k, Value::Str(_))) {
                let mut m: Vec<(String, Value)> = keys
                    .into_iter()
                    .map(|(k, v)| match k {
                        Value::Str(s) => (s, v),
                        other => (other.sprint(), v),
                    })
                    .collect();
                m.sort_by(|a, b| a.0.cmp(&b.0));
                Ok(Value::Map(m))
            } else {
                Ok(Value::AnyMap(keys))
            }
        }
    }
}

fn check_unique_keys(pairs: &[(Node, Node)]) -> Result<(), String> {
    for (i, (k, _)) in pairs.iter().enumerate() {
        let Node::Scalar { value, .. } = k else {
            continue;
        };
        for (prev, _) in &pairs[..i] {
            if let Node::Scalar { value: pv, .. } = prev
                && pv == value
            {
                return Err(format!(
                    "yaml: unmarshal errors:\n  line {}: mapping key {} already defined at line {}",
                    node_line(k),
                    crate::gofmt::quote(value),
                    node_line(prev)
                ));
            }
        }
    }
    Ok(())
}

fn short_tag(v: &Value) -> &'static str {
    match v {
        Value::Nil => "!!null",
        Value::Bool(_) => "!!bool",
        Value::Int(_) | Value::Uint(_) => "!!int",
        Value::Float(_) => "!!float",
        Value::Time(_) => "!!timestamp",
        _ => "!!str",
    }
}

/// `yaml.Unmarshal` into a `map[string]any`. `Ok(None)` is an empty or null
/// document (Go leaves the map nil).
pub fn unmarshal_map(src: &str) -> Result<Option<Vec<(String, Value)>>, String> {
    let mut b = Builder::default();
    let mut parser = Parser::new_from_str(src);
    parser
        .load(&mut b, false)
        .map_err(|e| format!("yaml: {e}"))?;
    let Some(root) = b.root else {
        return Ok(None);
    };
    match &root {
        Node::Map(pairs, _) => {
            check_unique_keys(pairs)?;
            let mut out: Vec<(String, Value)> = Vec::new();
            for (k, v) in pairs {
                // A key decodes into `string` from its source text.
                let key = match k {
                    Node::Scalar {
                        value, style, tag, ..
                    } => {
                        if !indicated_string(*style, tag) && resolve_plain(value) == Value::Nil {
                            String::new()
                        } else {
                            value.clone()
                        }
                    }
                    other => {
                        return Err(format!(
                            "yaml: unmarshal errors:\n  line {}: cannot unmarshal {} into string",
                            node_line(other),
                            if matches!(other, Node::Seq(..)) {
                                "!!seq"
                            } else {
                                "!!map"
                            }
                        ));
                    }
                };
                let val = to_any(v)?;
                match out.iter_mut().find(|(k, _)| *k == key) {
                    Some(slot) => slot.1 = val,
                    None => out.push((key, val)),
                }
            }
            out.sort_by(|a, b| a.0.cmp(&b.0));
            Ok(Some(out))
        }
        Node::Seq(_, line) => Err(format!(
            "yaml: unmarshal errors:\n  line {line}: cannot unmarshal !!seq into map[string]interface {{}}"
        )),
        Node::Scalar { value, line, .. } => {
            let v = to_any(&root)?;
            if v == Value::Nil {
                return Ok(None);
            }
            Err(format!(
                "yaml: unmarshal errors:\n  line {line}: cannot unmarshal {} `{value}` into map[string]interface {{}}",
                short_tag(&v)
            ))
        }
    }
}

#[cfg(test)]
mod tests;
