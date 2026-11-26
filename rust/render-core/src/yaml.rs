//! YAML the way `gopkg.in/yaml.v3` reads and writes it.
//!
//! `load` decodes the first document into a [`Value`] with v3's scalar resolution (YAML 1.2
//! booleans, YAML 1.1 octals, timestamps, merge keys, duplicate-key errors). The syntax itself
//! is parsed by `yaml-rust2`, a libyaml-style parser like the one inside v3, so documents both
//! accept decode the same way.
//!
//! `dump` is a port of v3's `Marshal` encoder and the parts of its libyaml emitter that a
//! decoded value can reach: block mappings and sequences at indent 4, `{}`/`[]` for empty ones,
//! plain/single/double-quoted/literal scalars with v3's style choice. v3 sets no line width,
//! so long scalars never fold.
//!
//! yaml.v3 is Apache-2.0 and its emitter MIT; licence and notice text: `rust/THIRD_PARTY_NOTICES`.

use std::collections::{BTreeMap, HashMap};
use std::sync::OnceLock;

use yaml_rust2::parser::Tag;
use yaml_rust2::parser::{Event, Parser};
use yaml_rust2::scanner::TScalarStyle;

use crate::gofmt;
use crate::goval::Value;

/// A `time.Time` from a YAML timestamp: civil fields plus a fixed UTC offset.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Time {
    /// Year (four digits when parsed).
    pub year: i32,
    month: u32,
    day: u32,
    hour: u32,
    minute: u32,
    second: u32,
    nanos: u32,
    offset_secs: i32,
}

impl Time {
    /// `t.Format(time.RFC3339Nano)`.
    pub fn rfc3339_nano(&self) -> String {
        let mut s = format!(
            "{:04}-{:02}-{:02}T{:02}:{:02}:{:02}",
            self.year, self.month, self.day, self.hour, self.minute, self.second
        );
        if self.nanos > 0 {
            let frac = format!("{:09}", self.nanos);
            s.push('.');
            s.push_str(frac.trim_end_matches('0'));
        }
        if self.offset_secs == 0 {
            s.push('Z');
        } else {
            let (sign, off) = if self.offset_secs < 0 {
                ('-', -self.offset_secs)
            } else {
                ('+', self.offset_secs)
            };
            s.push_str(&format!("{sign}{:02}:{:02}", off / 3600, off / 60 % 60));
        }
        s
    }
}

// ---------------------------------------------------------------------------------------------
// Timestamps: the four layouts yaml.v3 tries with Go's time.Parse.

fn is_leap(y: i32) -> bool {
    y % 4 == 0 && (y % 100 != 0 || y % 400 == 0)
}

fn days_in(month: u32, year: i32) -> u32 {
    match month {
        2 if is_leap(year) => 29,
        2 => 28,
        4 | 6 | 9 | 11 => 30,
        _ => 31,
    }
}

/// Go's `getnum(s, false)`: one or two leading digits.
fn getnum(s: &[u8]) -> Option<(u32, &[u8])> {
    let d0 = *s.first()?;
    if !d0.is_ascii_digit() {
        return None;
    }
    match s.get(1) {
        Some(d1) if d1.is_ascii_digit() => {
            Some((u32::from(d0 - b'0') * 10 + u32::from(d1 - b'0'), &s[2..]))
        }
        _ => Some((u32::from(d0 - b'0'), &s[1..])),
    }
}

fn getnum_fixed2(s: &[u8]) -> Option<u32> {
    if s.len() == 2 && s[0].is_ascii_digit() && s[1].is_ascii_digit() {
        Some(u32::from(s[0] - b'0') * 10 + u32::from(s[1] - b'0'))
    } else {
        None
    }
}

/// Go's `skip` for a literal layout chunk (a space matches a run of spaces).
fn skip<'a>(mut value: &'a [u8], mut prefix: &[u8]) -> Option<&'a [u8]> {
    while let Some(&p) = prefix.first() {
        if p == b' ' {
            if value.first().is_some_and(|&c| c != b' ') {
                return None;
            }
            while prefix.first() == Some(&b' ') {
                prefix = &prefix[1..];
            }
            while value.first() == Some(&b' ') {
                value = &value[1..];
            }
            continue;
        }
        if value.first() != Some(&p) {
            return None;
        }
        prefix = &prefix[1..];
        value = &value[1..];
    }
    Some(value)
}

#[derive(Clone, Copy)]
enum Layout {
    /// `2006-1-2T15:4:5.999999999Z07:00` with this separator.
    Zoned(u8),
    /// `2006-1-2 15:4:5.999999999`.
    Spaced,
    /// `2006-1-2`.
    Date,
}

fn parse_layout(s: &str, layout: Layout) -> Option<Time> {
    let v = s.as_bytes();
    if v.len() < 4 || !v[..4].iter().all(u8::is_ascii_digit) {
        return None;
    }
    let year: i32 = s[..4].parse().ok()?;
    let v = skip(&v[4..], b"-")?;
    let (month, v) = getnum(v)?;
    if !(1..=12).contains(&month) {
        return None;
    }
    let v = skip(v, b"-")?;
    let (day, mut v) = getnum(v)?;
    let mut t = Time {
        year,
        month,
        day,
        hour: 0,
        minute: 0,
        second: 0,
        nanos: 0,
        offset_secs: 0,
    };
    let sep: &[u8] = match layout {
        Layout::Zoned(b't') => b"t",
        Layout::Zoned(_) => b"T",
        Layout::Spaced => b" ",
        Layout::Date => b"",
    };
    if !matches!(layout, Layout::Date) {
        v = skip(v, sep)?;
        let (hour, rest) = getnum(v)?;
        if hour >= 24 {
            return None;
        }
        let rest = skip(rest, b":")?;
        let (minute, rest) = getnum(rest)?;
        if minute >= 60 {
            return None;
        }
        let rest = skip(rest, b":")?;
        let (second, mut rest) = getnum(rest)?;
        if second >= 60 {
            return None;
        }
        // .999999999: optional fraction, up to nine digits, '.' or ','.
        if rest.len() >= 2 && (rest[0] == b'.' || rest[0] == b',') && rest[1].is_ascii_digit() {
            let mut n = 1;
            while n < 10 && rest.get(n).is_some_and(u8::is_ascii_digit) {
                n += 1;
            }
            let digits = std::str::from_utf8(&rest[1..n]).ok()?;
            let mut nanos: u32 = digits.parse().ok()?;
            for _ in digits.len()..9 {
                nanos *= 10;
            }
            t.nanos = nanos;
            rest = &rest[n..];
        }
        (t.hour, t.minute, t.second) = (hour, minute, second);
        v = rest;
        if matches!(layout, Layout::Zoned(_)) {
            if v.first() == Some(&b'Z') {
                v = &v[1..];
            } else {
                if v.len() < 6 || v[3] != b':' {
                    return None;
                }
                let hr = getnum_fixed2(&v[1..3])?;
                let mm = getnum_fixed2(&v[4..6])?;
                let sign = match v[0] {
                    b'+' => 1,
                    b'-' => -1,
                    _ => return None,
                };
                if hr > 24 || mm > 60 {
                    return None;
                }
                t.offset_secs = sign * (hr as i32 * 3600 + mm as i32 * 60);
                v = &v[6..];
            }
        }
    }
    if !v.is_empty() || day < 1 || day > days_in(month, year) {
        return None;
    }
    Some(t)
}

fn parse_timestamp(s: &str) -> Option<Time> {
    let b = s.as_bytes();
    let i = b.iter().take_while(|c| c.is_ascii_digit()).count();
    if i != 4 || i == b.len() || b[i] != b'-' {
        return None;
    }
    [
        Layout::Zoned(b'T'),
        Layout::Zoned(b't'),
        Layout::Spaced,
        Layout::Date,
    ]
    .into_iter()
    .find_map(|l| parse_layout(s, l))
}

// ---------------------------------------------------------------------------------------------
// Scalar resolution (resolve.go).

const STR: &str = "!!str";
const NULL: &str = "!!null";
const BOOL: &str = "!!bool";
const INT: &str = "!!int";
const FLOAT: &str = "!!float";
const TIMESTAMP: &str = "!!timestamp";
const MERGE: &str = "!!merge";
const BINARY: &str = "!!binary";
const LONG_PREFIX: &str = "tag:yaml.org,2002:";

fn short_tag(tag: &str) -> String {
    match tag.strip_prefix(LONG_PREFIX) {
        Some(rest) => format!("!!{rest}"),
        None => tag.to_string(),
    }
}

/// `strconv.ParseInt(s, 0, 64)` / `ParseUint(s, 0, 64)` without underscores (already removed).
fn parse_int_base0(s: &str) -> (Option<i64>, Option<u64>) {
    let (neg, body) = match s.as_bytes().first() {
        Some(b'+') => (false, &s[1..]),
        Some(b'-') => (true, &s[1..]),
        _ => (false, s),
    };
    let lower = body.to_ascii_lowercase();
    let (radix, digits) = if let Some(d) = lower.strip_prefix("0x") {
        (16, d)
    } else if let Some(d) = lower.strip_prefix("0b") {
        (2, d)
    } else if let Some(d) = lower.strip_prefix("0o") {
        (8, d)
    } else if lower.len() > 1 && lower.starts_with('0') {
        (8, &lower[1..])
    } else {
        (10, lower.as_str())
    };
    if digits.is_empty() || !digits.chars().all(|c| c.is_digit(radix)) {
        return (None, None);
    }
    let mag = u64::from_str_radix(digits, radix).ok();
    let int = mag.and_then(|m| {
        if neg {
            if m <= 1 << 63 {
                Some((m as i64).wrapping_neg())
            } else {
                None
            }
        } else {
            i64::try_from(m).ok()
        }
    });
    // ParseUint takes no sign at all.
    let uint = if s.starts_with(['+', '-']) { None } else { mag };
    (int, uint)
}

fn yaml_style_float(s: &str) -> bool {
    static RE: OnceLock<regex::Regex> = OnceLock::new();
    RE.get_or_init(|| {
        regex::Regex::new(r"^[-+]?(\.[0-9]+|[0-9]+(\.[0-9]*)?)([eE][-+]?[0-9]+)?$")
            .unwrap_or_else(|_| unreachable!())
    })
    .is_match(s)
}

/// `strconv.ParseFloat(s, 64)` for the decimal forms YAML can reach. Overflow is an error in Go.
fn parse_float(s: &str) -> Option<f64> {
    let t = s.strip_prefix('+').unwrap_or(s);
    let ok = t.strip_prefix('-').unwrap_or(t);
    // Rust also accepts "inf"/"nan" spellings that cannot start with '.', '+', '-' or a digit
    // here except after a sign, which Go accepts too ("+inf"); keep them.
    let f: f64 = if ok.eq_ignore_ascii_case("inf")
        || ok.eq_ignore_ascii_case("infinity")
        || ok.eq_ignore_ascii_case("nan")
    {
        t.parse().ok()?
    } else {
        if !ok
            .bytes()
            .all(|c| c.is_ascii_digit() || matches!(c, b'.' | b'e' | b'E' | b'+' | b'-'))
        {
            return None;
        }
        let f: f64 = t.parse().ok()?;
        if f.is_infinite() {
            return None;
        }
        f
    };
    Some(f)
}

/// What `resolve` returns: the resolved tag and value, or a decode error.
fn resolve(tag: &str, s: &str) -> Result<(String, Value), String> {
    let tag = short_tag(tag);
    if !matches!(
        tag.as_str(),
        "" | STR | BOOL | INT | FLOAT | NULL | TIMESTAMP
    ) {
        return Ok((tag, Value::Str(s.to_string())));
    }
    let (rtag, out) = resolve_plain(&tag, s);
    match tag.as_str() {
        "" | STR => Ok((rtag, out)),
        _ if rtag == tag => Ok((rtag, out)),
        FLOAT if rtag == INT => match out {
            Value::Int(i) => Ok((FLOAT.to_string(), Value::Float(i as f64))),
            _ => Err(format!("cannot decode {rtag} `{s}` as a {tag}")),
        },
        _ => Err(format!("cannot decode {rtag} `{s}` as a {tag}")),
    }
}

fn resolve_plain(tag: &str, s: &str) -> (String, Value) {
    let hint = match s.as_bytes().first() {
        None => b'N',
        Some(b'+' | b'-') => b'S',
        Some(b'0'..=b'9') => b'D',
        Some(b'y' | b'Y' | b'n' | b'N' | b't' | b'T' | b'f' | b'F' | b'o' | b'O' | b'~') => b'M',
        Some(b'.') => b'.',
        Some(_) => 0,
    };
    if hint == 0 || tag == STR || tag == BINARY {
        return (STR.to_string(), Value::Str(s.to_string()));
    }
    match s {
        "true" | "True" | "TRUE" => return (BOOL.to_string(), Value::Bool(true)),
        "false" | "False" | "FALSE" => return (BOOL.to_string(), Value::Bool(false)),
        "" | "~" | "null" | "Null" | "NULL" => return (NULL.to_string(), Value::Null),
        ".nan" | ".NaN" | ".NAN" => return (FLOAT.to_string(), Value::Float(f64::NAN)),
        ".inf" | ".Inf" | ".INF" | "+.inf" | "+.Inf" | "+.INF" => {
            return (FLOAT.to_string(), Value::Float(f64::INFINITY));
        }
        "-.inf" | "-.Inf" | "-.INF" => return (FLOAT.to_string(), Value::Float(f64::NEG_INFINITY)),
        "<<" => return (MERGE.to_string(), Value::Str("<<".to_string())),
        _ => {}
    }
    match hint {
        b'.' => {
            if let Some(f) = parse_float(s) {
                return (FLOAT.to_string(), Value::Float(f));
            }
        }
        b'D' | b'S' => {
            if (tag.is_empty() || tag == TIMESTAMP)
                && let Some(t) = parse_timestamp(s)
            {
                return (TIMESTAMP.to_string(), Value::Time(t));
            }
            let plain = s.replace('_', "");
            let (int, uint) = parse_int_base0(&plain);
            if let Some(i) = int {
                return (INT.to_string(), Value::Int(i));
            }
            if let Some(u) = uint {
                return (INT.to_string(), Value::Uint(u));
            }
            if yaml_style_float(&plain)
                && let Some(f) = parse_float(&plain)
            {
                return (FLOAT.to_string(), Value::Float(f));
            }
        }
        _ => {}
    }
    (STR.to_string(), Value::Str(s.to_string()))
}

// ---------------------------------------------------------------------------------------------
// Loading: events to nodes (parser.go) to values (decode.go).

#[derive(Debug, Clone)]
enum Node {
    Scalar {
        value: String,
        style: TScalarStyle,
        tag: String,
    },
    Seq(Vec<Node>),
    Map {
        pairs: Vec<(Node, Node)>,
    },
    Alias(Box<Node>),
}

impl Node {
    fn indicated_string(&self) -> bool {
        match self {
            Node::Scalar { style, tag, .. } => {
                short_tag(tag) == STR
                    || ((tag.is_empty() || tag == "!") && !matches!(style, TScalarStyle::Plain))
            }
            _ => false,
        }
    }

    fn short_tag(&self) -> String {
        if self.indicated_string() {
            return STR.to_string();
        }
        match self {
            Node::Scalar { value, tag, .. } => {
                if tag.is_empty() || tag == "!" {
                    resolve_plain("", value).0
                } else {
                    short_tag(tag)
                }
            }
            Node::Seq(_) => "!!seq".to_string(),
            Node::Map { .. } => "!!map".to_string(),
            Node::Alias(n) => n.short_tag(),
        }
    }

    /// The (kind, value) pair v3 compares for duplicate keys.
    fn key_identity(&self) -> (u8, &str) {
        match self {
            Node::Scalar { value, .. } => (3, value),
            Node::Seq(_) => (2, ""),
            Node::Map { .. } => (4, ""),
            Node::Alias(_) => (5, ""),
        }
    }

    fn is_merge(&self) -> bool {
        matches!(self, Node::Scalar { value, tag, style: TScalarStyle::Plain } if value == "<<" && (tag.is_empty() || tag == "!" || short_tag(tag) == MERGE))
    }
}

fn tag_string(tag: Option<Tag>) -> String {
    match tag {
        None => String::new(),
        Some(t) => format!("{}{}", t.handle, t.suffix),
    }
}

struct Builder<'a> {
    parser: Parser<std::str::Chars<'a>>,
    anchors: HashMap<usize, Node>,
}

impl Builder<'_> {
    fn next(&mut self) -> Result<Event, String> {
        self.parser
            .next_token()
            .map(|(ev, _)| ev)
            .map_err(|e| e.to_string())
    }

    fn node(&mut self, ev: Event) -> Result<Node, String> {
        let (node, anchor) = match ev {
            Event::Alias(id) => {
                let target = self
                    .anchors
                    .get(&id)
                    .cloned()
                    .ok_or_else(|| "unknown anchor".to_string())?;
                return Ok(Node::Alias(Box::new(target)));
            }
            Event::Scalar(value, style, anchor, tag) => (
                Node::Scalar {
                    value,
                    style,
                    tag: tag_string(tag),
                },
                anchor,
            ),
            Event::SequenceStart(anchor, _) => {
                let mut items = Vec::new();
                loop {
                    let ev = self.next()?;
                    if ev == Event::SequenceEnd {
                        break;
                    }
                    items.push(self.node(ev)?);
                }
                (Node::Seq(items), anchor)
            }
            Event::MappingStart(anchor, _) => {
                let mut pairs = Vec::new();
                loop {
                    let ev = self.next()?;
                    if ev == Event::MappingEnd {
                        break;
                    }
                    let k = self.node(ev)?;
                    let ev = self.next()?;
                    let v = self.node(ev)?;
                    pairs.push((k, v));
                }
                (Node::Map { pairs }, anchor)
            }
            other => return Err(format!("unexpected event {other:?}")),
        };
        if anchor > 0 {
            self.anchors.insert(anchor, node.clone());
        }
        Ok(node)
    }
}

/// Parses the first YAML document of `s` into a node, or `None` for an empty stream.
fn parse_first_document(s: &str) -> Result<Option<Node>, String> {
    let mut b = Builder {
        parser: Parser::new_from_str(s),
        anchors: HashMap::new(),
    };
    loop {
        match b.next()? {
            Event::StreamStart | Event::Nothing => {}
            Event::StreamEnd => return Ok(None),
            Event::DocumentStart => {
                let ev = b.next()?;
                if ev == Event::DocumentEnd {
                    return Ok(None);
                }
                let node = b.node(ev)?;
                // The document must close cleanly, as v3's parser expects DOCUMENT-END.
                match b.next()? {
                    Event::DocumentEnd => return Ok(Some(node)),
                    other => return Err(format!("unexpected event {other:?}")),
                }
            }
            other => return Err(format!("unexpected event {other:?}")),
        }
    }
}

/// How a value is being decoded into: `any`, or the key type of a `map[string]any`.
#[derive(Clone, Copy, PartialEq)]
enum Target {
    Any,
    String,
}

/// A map under construction: `map[string]any` or `map[any]any`.
enum OutMap {
    Strings(BTreeMap<String, Value>),
    Any(Vec<(Value, Value)>),
}

impl OutMap {
    fn set(&mut self, k: Value, v: Value) -> Result<(), String> {
        match self {
            OutMap::Strings(m) => match k {
                Value::Str(s) => {
                    m.insert(s, v);
                    Ok(())
                }
                _ => Err("non-string key in a string map".to_string()),
            },
            OutMap::Any(entries) => {
                if let Some(e) = entries.iter_mut().find(|(x, _)| *x == k) {
                    e.1 = v;
                } else {
                    entries.push((k, v));
                }
                Ok(())
            }
        }
    }
}

fn decode(n: &Node, target: Target) -> Result<Value, String> {
    match n {
        Node::Alias(inner) => decode(inner, target),
        Node::Scalar { value, tag, .. } => {
            if target == Target::String {
                // Decoding into a string takes the raw text (or the decoded !!binary bytes).
                if !n.indicated_string() && short_tag(tag) == BINARY {
                    return decode_binary(value);
                }
                if n.short_tag() == NULL {
                    return Ok(Value::Str(String::new()));
                }
                return Ok(Value::Str(value.clone()));
            }
            if n.indicated_string() {
                return Ok(Value::Str(value.clone()));
            }
            let (rtag, v) = resolve(tag, value)?;
            if rtag == BINARY {
                return decode_binary(value);
            }
            Ok(v)
        }
        Node::Seq(items) => {
            if target == Target::String {
                return Err("cannot unmarshal !!seq into string".to_string());
            }
            items
                .iter()
                .map(|i| decode(i, Target::Any))
                .collect::<Result<_, _>>()
                .map(Value::Seq)
        }
        Node::Map { pairs } => {
            if target == Target::String {
                return Err("cannot unmarshal !!map into string".to_string());
            }
            check_duplicates(pairs)?;
            let string_map = pairs
                .iter()
                .all(|(k, _)| matches!(k.short_tag().as_str(), STR | MERGE));
            let mut out = if string_map {
                OutMap::Strings(BTreeMap::new())
            } else {
                OutMap::Any(Vec::new())
            };
            decode_mapping_into(pairs, &mut out, None)?;
            Ok(match out {
                OutMap::Strings(m) => Value::Map(m),
                OutMap::Any(v) => Value::AnyMap(v),
            })
        }
    }
}

fn decode_binary(value: &str) -> Result<Value, String> {
    let bytes = base64_decode(value).ok_or("!!binary value contains invalid base64 data")?;
    String::from_utf8(bytes)
        .map(Value::Str)
        .map_err(|e| format!("!!binary value is not UTF-8: {e}"))
}

fn base64_decode(s: &str) -> Option<Vec<u8>> {
    let mut out = Vec::new();
    let mut acc = 0u32;
    let mut bits = 0;
    let s = s.as_bytes();
    let body_len = s.iter().rposition(|&c| c != b'=').map_or(0, |p| p + 1);
    if !s.len().is_multiple_of(4) || s.len() - body_len > 2 {
        return None;
    }
    for &c in &s[..body_len] {
        let v = match c {
            b'A'..=b'Z' => c - b'A',
            b'a'..=b'z' => c - b'a' + 26,
            b'0'..=b'9' => c - b'0' + 52,
            b'+' => 62,
            b'/' => 63,
            _ => return None,
        };
        acc = (acc << 6) | u32::from(v);
        bits += 6;
        if bits >= 8 {
            bits -= 8;
            out.push((acc >> bits) as u8);
        }
    }
    Some(out)
}

fn check_duplicates(pairs: &[(Node, Node)]) -> Result<(), String> {
    for (i, (ki, _)) in pairs.iter().enumerate() {
        for (kj, _) in &pairs[i + 1..] {
            if ki.key_identity() == kj.key_identity() {
                return Err(format!(
                    "mapping key {:?} already defined",
                    ki.key_identity().1
                ));
            }
        }
    }
    Ok(())
}

fn key_target(out: &OutMap) -> Target {
    match out {
        OutMap::Strings(_) => Target::String,
        OutMap::Any(_) => Target::Any,
    }
}

/// `decoder.mapping` into an existing map. `merged` is v3's `mergedFields`: keys a merge must
/// not overwrite.
fn decode_mapping_into(
    pairs: &[(Node, Node)],
    out: &mut OutMap,
    mut merged: Option<&mut Vec<Value>>,
) -> Result<(), String> {
    let mut merge_node = None;
    for (k, v) in pairs {
        if k.is_merge() {
            merge_node = Some(v);
            continue;
        }
        let key = decode(k, key_target(out))?;
        if matches!(key, Value::Seq(_) | Value::Map(_) | Value::AnyMap(_)) {
            return Err("invalid map key".to_string());
        }
        if let Some(m) = merged.as_deref_mut() {
            if m.contains(&key) {
                continue;
            }
            m.push(key.clone());
        }
        let val = decode(v, Target::Any)?;
        out.set(key, val)?;
    }
    if let Some(m) = merge_node {
        // The parent's own keys win over merged ones.
        let mut fields = Vec::new();
        for (k, _) in pairs {
            if let Ok(key) = decode(k, Target::Any) {
                fields.push(key);
            }
        }
        let fields = match merged {
            Some(f) => f,
            None => &mut fields,
        };
        let want_map = || "map merge requires map or sequence of maps as the value".to_string();
        let as_map = |n: &Node| -> Option<Vec<(Node, Node)>> {
            match n {
                Node::Map { pairs } => Some(pairs.clone()),
                Node::Alias(inner) => match inner.as_ref() {
                    Node::Map { pairs } => Some(pairs.clone()),
                    _ => None,
                },
                _ => None,
            }
        };
        match m {
            Node::Seq(items) => {
                for item in items {
                    let p = as_map(item).ok_or_else(want_map)?;
                    decode_mapping_into(&p, out, Some(fields))?;
                }
            }
            other => {
                let p = as_map(other).ok_or_else(want_map)?;
                decode_mapping_into(&p, out, Some(fields))?;
            }
        }
    }
    Ok(())
}

/// `yaml.Unmarshal(s, &v)` with `v any`: the first document's value, `Null` for an empty
/// stream, or an error.
pub fn load(s: &str) -> Result<Value, String> {
    match parse_first_document(s)? {
        None => Ok(Value::Null),
        Some(n) => decode(&n, Target::Any),
    }
}

// ---------------------------------------------------------------------------------------------
// Map key order (sorter.go).

fn unicode_ranges(class: &str) -> Vec<(u32, u32)> {
    let hir = regex_syntax::Parser::new().parse(class);
    match hir.as_ref().map(regex_syntax::hir::Hir::kind) {
        Ok(regex_syntax::hir::HirKind::Class(regex_syntax::hir::Class::Unicode(c))) => c
            .iter()
            .map(|r| (r.start() as u32, r.end() as u32))
            .collect(),
        _ => Vec::new(),
    }
}

fn in_ranges(ranges: &[(u32, u32)], c: char) -> bool {
    let c = c as u32;
    let i = ranges.partition_point(|&(_, hi)| hi < c);
    ranges.get(i).is_some_and(|&(lo, _)| lo <= c)
}

/// `unicode.IsDigit`.
fn is_digit(c: char) -> bool {
    static ND: OnceLock<Vec<(u32, u32)>> = OnceLock::new();
    if (c as u32) <= 0xFF {
        return c.is_ascii_digit();
    }
    in_ranges(ND.get_or_init(|| unicode_ranges(r"\p{Nd}")), c)
}

/// `unicode.IsLetter`.
fn is_letter(c: char) -> bool {
    static L: OnceLock<Vec<(u32, u32)>> = OnceLock::new();
    in_ranges(L.get_or_init(|| unicode_ranges(r"\p{L}")), c)
}

/// The `reflect.Kind` order v3 falls back on for keys of different types.
fn kind(v: &Value) -> u8 {
    match v {
        Value::Bool(_) => 1,
        Value::Int(_) => 2,
        Value::Uint(_) => 11,
        Value::Float(_) => 14,
        Value::Null => 20,
        Value::Map(_) | Value::AnyMap(_) => 21,
        Value::Seq(_) => 23,
        Value::Str(_) => 24,
        Value::Time(_) => 25,
    }
}

fn key_float(v: &Value) -> Option<f64> {
    match v {
        Value::Int(i) => Some(*i as f64),
        Value::Uint(u) => Some(*u as f64),
        Value::Float(f) => Some(*f),
        Value::Bool(b) => Some(if *b { 1.0 } else { 0.0 }),
        _ => None,
    }
}

/// v3's `keyList.Less`.
fn key_less(a: &Value, b: &Value) -> bool {
    let (ak, bk) = (kind(a), kind(b));
    if let (Some(af), Some(bf)) = (key_float(a), key_float(b)) {
        if af != bf {
            return af < bf;
        }
        if ak != bk {
            return ak < bk;
        }
        return match (a, b) {
            (Value::Int(x), Value::Int(y)) => x < y,
            (Value::Uint(x), Value::Uint(y)) => x < y,
            (Value::Float(x), Value::Float(y)) => x < y,
            (Value::Bool(x), Value::Bool(y)) => !x && *y,
            _ => false,
        };
    }
    let (Value::Str(a), Value::Str(b)) = (a, b) else {
        return ak < bk;
    };
    string_less(a, b)
}

fn string_less(a: &str, b: &str) -> bool {
    let ar: Vec<char> = a.chars().collect();
    let br: Vec<char> = b.chars().collect();
    let mut digits = false;
    let mut i = 0;
    while i < ar.len() && i < br.len() {
        if ar[i] == br[i] {
            digits = is_digit(ar[i]);
            i += 1;
            continue;
        }
        let al = is_letter(ar[i]);
        let bl = is_letter(br[i]);
        if al && bl {
            return ar[i] < br[i];
        }
        if al || bl {
            return if digits { al } else { bl };
        }
        let (mut an, mut bn): (i64, i64) = (0, 0);
        if ar[i] == '0' || br[i] == '0' {
            let mut j = i;
            while j > 0 && is_digit(ar[j - 1]) {
                j -= 1;
                if ar[j] != '0' {
                    an = 1;
                    bn = 1;
                    break;
                }
            }
        }
        let mut ai = i;
        while ai < ar.len() && is_digit(ar[ai]) {
            an = an
                .wrapping_mul(10)
                .wrapping_add(i64::from(ar[ai] as u32) - i64::from(b'0'));
            ai += 1;
        }
        let mut bi = i;
        while bi < br.len() && is_digit(br[bi]) {
            bn = bn
                .wrapping_mul(10)
                .wrapping_add(i64::from(br[bi] as u32) - i64::from(b'0'));
            bi += 1;
        }
        if an != bn {
            return an < bn;
        }
        if ai != bi {
            return ai < bi;
        }
        return ar[i] < br[i];
    }
    ar.len() < br.len()
}

/// A stable merge sort by a `less` function. It never panics on a `less` that is not a strict
/// weak order (v3's key order is not always one), where `slice::sort_by` may.
fn merge_sort<T: Clone>(v: &mut [T], less: &impl Fn(&T, &T) -> bool) {
    if v.len() <= 1 {
        return;
    }
    let mid = v.len() / 2;
    merge_sort(&mut v[..mid], less);
    merge_sort(&mut v[mid..], less);
    let (left, right) = (v[..mid].to_vec(), v[mid..].to_vec());
    let (mut i, mut j, mut k) = (0, 0, 0);
    while i < left.len() && j < right.len() {
        if less(&right[j], &left[i]) {
            v[k] = right[j].clone();
            j += 1;
        } else {
            v[k] = left[i].clone();
            i += 1;
        }
        k += 1;
    }
    for x in left[i..].iter().chain(&right[j..]) {
        v[k] = x.clone();
        k += 1;
    }
}

// ---------------------------------------------------------------------------------------------
// Dumping: v3's encoder and libyaml emitter.

/// v3's indent. It sets no line width (libyaml then treats the width as unlimited), so the
/// writers below never fold a long scalar and libyaml's folding branches are left out.
const BEST_INDENT: i32 = 4;

#[derive(Clone, Copy, PartialEq)]
enum Style {
    Plain,
    Single,
    Double,
    Literal,
}

#[derive(Clone, Copy, PartialEq)]
enum Parent {
    Document,
    Sequence,
    Mapping,
}

struct Analysis {
    multiline: bool,
    flow_plain_allowed: bool,
    block_plain_allowed: bool,
    single_quoted_allowed: bool,
    block_allowed: bool,
}

fn is_break_char(c: char) -> bool {
    matches!(c, '\r' | '\n' | '\u{85}' | '\u{2028}' | '\u{2029}')
}

/// libyaml's `is_printable` on a valid UTF-8 character. It only looks at one- to three-byte
/// sequences, so every character above U+FFFF (emoji included) counts as unprintable and is
/// escaped in double quotes.
fn is_printable(c: char) -> bool {
    let c = c as u32;
    c == 0x0A
        || (0x20..=0x7E).contains(&c)
        || (0xA0..=0xD7FF).contains(&c)
        || ((0xE000..=0xFFFD).contains(&c) && c != 0xFEFF)
}

fn analyze_scalar(value: &str) -> Analysis {
    if value.is_empty() {
        return Analysis {
            multiline: false,
            flow_plain_allowed: false,
            block_plain_allowed: true,
            single_quoted_allowed: true,
            block_allowed: false,
        };
    }
    let mut block_indicators = false;
    let mut flow_indicators = false;
    let mut line_breaks = false;
    let mut special_characters = false;
    let mut tab_characters = false;
    let mut leading_space = false;
    let mut leading_break = false;
    let mut trailing_space = false;
    let mut trailing_break = false;
    let mut break_space = false;
    let mut space_break = false;
    let mut previous_space = false;
    let mut previous_break = false;

    if value.starts_with("---") || value.starts_with("...") {
        block_indicators = true;
        flow_indicators = true;
    }
    let chars: Vec<char> = value.chars().collect();
    let mut preceded_by_whitespace = true;
    for (i, &c) in chars.iter().enumerate() {
        let next = chars.get(i + 1).copied();
        let followed_by_whitespace = next.is_none_or(|n| n == ' ' || n == '\t');
        if i == 0 {
            match c {
                '#' | ',' | '[' | ']' | '{' | '}' | '&' | '*' | '!' | '|' | '>' | '\'' | '"'
                | '%' | '@' | '`' => {
                    flow_indicators = true;
                    block_indicators = true;
                }
                '?' | ':' => {
                    flow_indicators = true;
                    if followed_by_whitespace {
                        block_indicators = true;
                    }
                }
                '-' if followed_by_whitespace => {
                    flow_indicators = true;
                    block_indicators = true;
                }
                _ => {}
            }
        } else {
            match c {
                ',' | '?' | '[' | ']' | '{' | '}' => flow_indicators = true,
                ':' => {
                    flow_indicators = true;
                    if followed_by_whitespace {
                        block_indicators = true;
                    }
                }
                '#' if preceded_by_whitespace => {
                    flow_indicators = true;
                    block_indicators = true;
                }
                _ => {}
            }
        }
        if c == '\t' {
            tab_characters = true;
        } else if !is_printable(c) {
            special_characters = true;
        }
        let last = i + 1 == chars.len();
        if c == ' ' {
            if i == 0 {
                leading_space = true;
            }
            if last {
                trailing_space = true;
            }
            if previous_break {
                break_space = true;
            }
            previous_space = true;
            previous_break = false;
        } else if is_break_char(c) {
            line_breaks = true;
            if i == 0 {
                leading_break = true;
            }
            if last {
                trailing_break = true;
            }
            if previous_space {
                space_break = true;
            }
            previous_space = false;
            previous_break = true;
        } else {
            previous_space = false;
            previous_break = false;
        }
        preceded_by_whitespace = c == ' ' || c == '\t' || is_break_char(c) || c == '\0';
    }
    let mut a = Analysis {
        multiline: line_breaks,
        flow_plain_allowed: true,
        block_plain_allowed: true,
        single_quoted_allowed: true,
        block_allowed: true,
    };
    if leading_space || leading_break || trailing_space || trailing_break {
        a.flow_plain_allowed = false;
        a.block_plain_allowed = false;
    }
    if trailing_space {
        a.block_allowed = false;
    }
    if break_space {
        a.flow_plain_allowed = false;
        a.block_plain_allowed = false;
        a.single_quoted_allowed = false;
    }
    if space_break || tab_characters || special_characters {
        a.flow_plain_allowed = false;
        a.block_plain_allowed = false;
        a.single_quoted_allowed = false;
    }
    if space_break || special_characters {
        a.block_allowed = false;
    }
    if line_breaks {
        a.flow_plain_allowed = false;
        a.block_plain_allowed = false;
    }
    if flow_indicators {
        a.flow_plain_allowed = false;
    }
    if block_indicators {
        a.block_plain_allowed = false;
    }
    a
}

fn is_base60_float(s: &str) -> bool {
    static RE: OnceLock<regex::Regex> = OnceLock::new();
    let Some(&c) = s.as_bytes().first() else {
        return false;
    };
    if !(c == b'+' || c == b'-' || c.is_ascii_digit()) || !s.contains(':') {
        return false;
    }
    RE.get_or_init(|| {
        regex::Regex::new(r"^[-+]?[0-9][0-9_]*(?::[0-5]?[0-9])+(?:\.[0-9_]*)?$")
            .unwrap_or_else(|_| unreachable!())
    })
    .is_match(s)
}

fn is_old_bool(s: &str) -> bool {
    matches!(
        s,
        "y" | "Y"
            | "yes"
            | "Yes"
            | "YES"
            | "on"
            | "On"
            | "ON"
            | "n"
            | "N"
            | "no"
            | "No"
            | "NO"
            | "off"
            | "Off"
            | "OFF"
    )
}

struct Emitter {
    out: String,
    indent: i32,
    indents: Vec<i32>,
    column: i32,
    whitespace: bool,
    indention: bool,
    flow_level: i32,
}

impl Emitter {
    fn put(&mut self, c: char) {
        self.out.push(c);
        self.column += 1;
    }

    fn put_break(&mut self) {
        self.out.push('\n');
        self.column = 0;
        self.indention = true;
    }

    fn write_break(&mut self, c: char) {
        if c == '\n' {
            self.put_break();
        } else {
            self.out.push(c);
            self.column = 0;
            self.indention = true;
        }
    }

    fn write_indent(&mut self) {
        let indent = self.indent.max(0);
        if !self.indention || self.column > indent || (self.column == indent && !self.whitespace) {
            self.put_break();
        }
        while self.column < indent {
            self.put(' ');
        }
        self.whitespace = true;
    }

    fn write_indicator(
        &mut self,
        ind: &str,
        need_whitespace: bool,
        is_whitespace: bool,
        is_indention: bool,
    ) {
        if need_whitespace && !self.whitespace {
            self.put(' ');
        }
        for c in ind.chars() {
            self.put(c);
        }
        self.whitespace = is_whitespace;
        self.indention = self.indention && is_indention;
    }

    fn increase_indent(&mut self, flow: bool, parent: Parent) {
        self.indents.push(self.indent);
        if self.indent < 0 {
            self.indent = if flow { BEST_INDENT } else { 0 };
        } else if parent == Parent::Sequence {
            self.indent += 2;
        } else {
            self.indent = BEST_INDENT * ((self.indent + BEST_INDENT) / BEST_INDENT);
        }
    }

    fn pop_indent(&mut self) {
        self.indent = self.indents.pop().unwrap_or(-1);
    }

    fn node(&mut self, v: &Value, parent: Parent, simple_key: bool) {
        match v {
            Value::Seq(items) => {
                if items.is_empty() || self.flow_level > 0 {
                    self.empty_flow("[", "]", parent);
                    return;
                }
                self.increase_indent(false, parent);
                for item in items {
                    self.write_indent();
                    self.write_indicator("-", true, false, true);
                    self.node(item, Parent::Sequence, false);
                }
                self.pop_indent();
            }
            Value::Map(m) => {
                let mut keys: Vec<&String> = m.keys().collect();
                merge_sort(&mut keys, &|a: &&String, b: &&String| string_less(a, b));
                let pairs: Vec<(Value, &Value)> = keys
                    .into_iter()
                    .map(|k| (Value::Str(k.clone()), &m[k]))
                    .collect();
                self.mapping(&pairs, parent);
            }
            Value::AnyMap(entries) => {
                let mut sorted: Vec<&(Value, Value)> = entries.iter().collect();
                merge_sort(&mut sorted, &|a: &&(Value, Value), b: &&(Value, Value)| {
                    key_less(&a.0, &b.0)
                });
                let pairs: Vec<(Value, &Value)> =
                    sorted.into_iter().map(|(k, v)| (k.clone(), v)).collect();
                self.mapping(&pairs, parent);
            }
            _ => {
                let (text, style) = scalar_text(v);
                self.scalar(&text, style, parent, simple_key);
            }
        }
    }

    fn empty_flow(&mut self, open: &str, close: &str, parent: Parent) {
        self.write_indicator(open, true, true, false);
        self.increase_indent(true, parent);
        self.pop_indent();
        if self.column == 0 {
            self.write_indent();
        }
        self.write_indicator(close, false, false, false);
    }

    fn mapping(&mut self, pairs: &[(Value, &Value)], parent: Parent) {
        if pairs.is_empty() || self.flow_level > 0 {
            self.empty_flow("{", "}", parent);
            return;
        }
        self.increase_indent(false, parent);
        for (k, v) in pairs {
            self.write_indent();
            let simple = match k {
                Value::Seq(s) => s.is_empty(),
                Value::Map(m) => m.is_empty(),
                Value::AnyMap(m) => m.is_empty(),
                _ => {
                    let (text, _) = scalar_text(k);
                    !analyze_scalar(&text).multiline && text.len() <= 128
                }
            };
            if simple {
                self.node(k, Parent::Mapping, true);
                self.write_indicator(":", false, false, false);
            } else {
                self.write_indicator("?", true, false, true);
                self.node(k, Parent::Mapping, false);
                self.write_indent();
                self.write_indicator(":", true, false, true);
            }
            self.node(v, Parent::Mapping, false);
        }
        self.pop_indent();
    }

    fn scalar(&mut self, value: &str, requested: Style, parent: Parent, simple_key: bool) {
        let a = analyze_scalar(value);
        let mut style = requested;
        if simple_key && a.multiline {
            style = Style::Double;
        }
        if style == Style::Plain {
            if (self.flow_level > 0 && !a.flow_plain_allowed)
                || (self.flow_level == 0 && !a.block_plain_allowed)
            {
                style = Style::Single;
            }
            if value.is_empty() && (self.flow_level > 0 || simple_key) {
                style = Style::Single;
            }
        }
        if style == Style::Single && !a.single_quoted_allowed {
            style = Style::Double;
        }
        if style == Style::Literal && (!a.block_allowed || self.flow_level > 0 || simple_key) {
            style = Style::Double;
        }
        self.increase_indent(true, parent);
        match style {
            Style::Plain => self.write_plain(value),
            Style::Single => self.write_single(value),
            Style::Double => self.write_double(value),
            Style::Literal => self.write_literal(value),
        }
        self.pop_indent();
    }

    fn write_plain(&mut self, value: &str) {
        if !value.is_empty() && !self.whitespace {
            self.put(' ');
        }
        let mut breaks = false;
        for c in value.chars() {
            if c == ' ' {
                self.put(c);
            } else if is_break_char(c) {
                if !breaks && c == '\n' {
                    self.put_break();
                }
                self.write_break(c);
                breaks = true;
            } else {
                if breaks {
                    self.write_indent();
                }
                self.put(c);
                self.indention = false;
                breaks = false;
            }
        }
        if !value.is_empty() {
            self.whitespace = false;
        }
        self.indention = false;
    }

    fn write_single(&mut self, value: &str) {
        self.write_indicator("'", true, false, false);
        let mut breaks = false;
        for c in value.chars() {
            if c == ' ' {
                self.put(c);
            } else if is_break_char(c) {
                if !breaks && c == '\n' {
                    self.put_break();
                }
                self.write_break(c);
                breaks = true;
            } else {
                if breaks {
                    self.write_indent();
                }
                if c == '\'' {
                    self.put('\'');
                }
                self.put(c);
                self.indention = false;
                breaks = false;
            }
        }
        self.write_indicator("'", false, false, false);
        self.whitespace = false;
        self.indention = false;
    }

    fn write_double(&mut self, value: &str) {
        self.write_indicator("\"", true, false, false);
        let bom = value.starts_with('\u{FEFF}');
        for c in value.chars() {
            if !is_printable(c) || bom || is_break_char(c) || c == '"' || c == '\\' {
                self.put('\\');
                let v = c as u32;
                match c {
                    '\0' => self.put('0'),
                    '\u{7}' => self.put('a'),
                    '\u{8}' => self.put('b'),
                    '\t' => self.put('t'),
                    '\n' => self.put('n'),
                    '\u{b}' => self.put('v'),
                    '\u{c}' => self.put('f'),
                    '\r' => self.put('r'),
                    '\u{1b}' => self.put('e'),
                    '"' => self.put('"'),
                    '\\' => self.put('\\'),
                    '\u{85}' => self.put('N'),
                    '\u{a0}' => self.put('_'),
                    '\u{2028}' => self.put('L'),
                    '\u{2029}' => self.put('P'),
                    _ => {
                        let (p, w) = if v <= 0xFF {
                            ('x', 2)
                        } else if v <= 0xFFFF {
                            ('u', 4)
                        } else {
                            ('U', 8)
                        };
                        self.put(p);
                        for k in (0..w).rev() {
                            let d = (v >> (k * 4)) & 0xF;
                            self.put(char::from_digit(d, 16).unwrap_or('0').to_ascii_uppercase());
                        }
                    }
                }
            } else {
                self.put(c);
            }
        }
        self.write_indicator("\"", false, false, false);
        self.whitespace = false;
        self.indention = false;
    }

    fn write_literal(&mut self, value: &str) {
        self.write_indicator("|", true, false, false);
        // Block scalar hints.
        let chars: Vec<char> = value.chars().collect();
        if chars.first().is_some_and(|&c| c == ' ' || is_break_char(c)) {
            let hint = char::from_digit(BEST_INDENT as u32, 10).unwrap_or('4');
            self.write_indicator(&hint.to_string(), false, false, false);
        }
        let chomp = match chars.len() {
            0 => Some('-'),
            n if !is_break_char(chars[n - 1]) => Some('-'),
            1 => Some('+'),
            n if is_break_char(chars[n - 2]) => Some('+'),
            _ => None,
        };
        if let Some(c) = chomp {
            self.write_indicator(&c.to_string(), false, false, false);
        }
        self.whitespace = true;
        let mut breaks = true;
        for &c in &chars {
            if is_break_char(c) {
                self.write_break(c);
                breaks = true;
            } else {
                if breaks {
                    self.write_indent();
                }
                self.put(c);
                self.indention = false;
                breaks = false;
            }
        }
    }
}

/// The text and requested style v3's encoder gives a scalar value.
fn scalar_text(v: &Value) -> (String, Style) {
    match v {
        Value::Null => ("null".to_string(), Style::Plain),
        Value::Bool(b) => (b.to_string(), Style::Plain),
        Value::Int(i) => (i.to_string(), Style::Plain),
        Value::Uint(u) => (u.to_string(), Style::Plain),
        Value::Float(f) => {
            let s = match gofmt::format_g(*f).as_str() {
                "+Inf" => ".inf".to_string(),
                "-Inf" => "-.inf".to_string(),
                "NaN" => ".nan".to_string(),
                s => s.to_string(),
            };
            (s, Style::Plain)
        }
        Value::Time(t) => (t.rfc3339_nano(), Style::Plain),
        Value::Str(s) => {
            let can_plain = matches!(resolve("", s), Ok((tag, _)) if tag == STR)
                && !(is_base60_float(s) || is_old_bool(s));
            let style = if s.contains('\n') {
                Style::Literal
            } else if can_plain {
                Style::Plain
            } else {
                Style::Double
            };
            (s.clone(), style)
        }
        Value::Seq(_) | Value::Map(_) | Value::AnyMap(_) => (String::new(), Style::Plain),
    }
}

/// `yaml.Marshal(v)`.
pub fn dump(v: &Value) -> String {
    let mut e = Emitter {
        out: String::new(),
        indent: -1,
        indents: Vec::new(),
        column: 0,
        whitespace: true,
        indention: true,
        flow_level: 0,
    };
    e.node(v, Parent::Document, false);
    // Document end: a final line break unless the output already ends at column 0.
    e.indent = -1;
    e.write_indent();
    e.out
}

#[cfg(test)]
mod tests;
