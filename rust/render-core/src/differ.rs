//! Turns one attribute's before/after values into a field: inline leaves, or a block with an
//! ordered variant ladder from most to least detail (`internal/differ`).

use std::sync::OnceLock;

use crate::difflib::{split_lines, unified_diff};
use crate::gofmt;
use crate::goval::{self, Value};
use crate::model::{Field, Leaf, LeafOp, Level, Variant};
use crate::yaml;

#[derive(Clone, Copy, PartialEq)]
enum ValueType {
    Plain,
    Json,
    Yaml,
    Base64,
}

fn base64_re() -> &'static regex::Regex {
    static RE: OnceLock<regex::Regex> = OnceLock::new();
    RE.get_or_init(|| {
        regex::Regex::new(r"^[A-Za-z0-9+/\r\n]+={0,2}$").unwrap_or_else(|_| unreachable!())
    })
}

/// Go's `base64.StdEncoding.DecodeString` succeeds (strict padding, `\r` and `\n` ignored).
fn base64_decodes(s: &str) -> bool {
    let body: Vec<u8> = s.bytes().filter(|&c| c != b'\r' && c != b'\n').collect();
    if !body.len().is_multiple_of(4) {
        return false;
    }
    let pad = body.iter().rev().take_while(|&&c| c == b'=').count();
    if pad > 2 || body[..body.len() - pad].contains(&b'=') {
        return false;
    }
    let is_b64 = |c: &u8| c.is_ascii_alphanumeric() || *c == b'+' || *c == b'/';
    if !body[..body.len() - pad].iter().all(is_b64) {
        return false;
    }
    // Go rejects non-zero trailing bits only in strict mode, which StdEncoding is not.
    true
}

/// Go's `strings.TrimSpace` (Unicode white space).
fn trim_space(s: &str) -> &str {
    s.trim_matches(|c: char| c.is_whitespace() || c == '\u{85}' || c == '\u{a0}')
}

fn detect(s: &str) -> ValueType {
    let trimmed = trim_space(s);
    if trimmed.is_empty() {
        return ValueType::Plain;
    }
    if (trimmed.starts_with('{') || trimmed.starts_with('['))
        && crate::gojson::check_valid(trimmed.as_bytes()).is_ok()
    {
        return ValueType::Json;
    }
    if trimmed.len() >= 100
        && base64_re().is_match(trimmed)
        && base64_decodes(&trimmed.replace('\n', ""))
    {
        return ValueType::Base64;
    }
    if let Ok(v) = yaml::load(s)
        && matches!(v, Value::Map(_) | Value::AnyMap(_) | Value::Seq(_))
    {
        return ValueType::Yaml;
    }
    ValueType::Plain
}

fn scalar(v: &Value) -> String {
    match v {
        Value::Null => "null".to_string(),
        Value::Float(f) => match gofmt::as_exact_i64(*f) {
            Some(i) => i.to_string(),
            None => gofmt::format_g(*f),
        },
        Value::Str(s) => gofmt::quote(s),
        Value::Bool(b) => b.to_string(),
        other => format!("{other:?}"),
    }
}

fn first_str(v: &Value) -> &str {
    v.as_str().unwrap_or("")
}

fn first_non_empty<'a>(a: &'a str, b: &'a str) -> &'a str {
    if !trim_space(a).is_empty() { a } else { b }
}

fn is_structured(before: &Value, after: &Value) -> bool {
    before.is_structured() || after.is_structured()
}

fn structured_kind(in_: &Input) -> &'static str {
    let s = first_non_empty(first_str(&in_.before), first_str(&in_.after));
    if !s.is_empty() {
        match detect(s) {
            ValueType::Json => return "json",
            ValueType::Yaml => return "yaml",
            _ => {}
        }
    }
    "yaml"
}

fn canonical(v: &Value, kind: &str) -> String {
    if matches!(v, Value::Null) {
        return String::new();
    }
    if kind == "json"
        && let Some(s) = goval::marshal_indent(v)
    {
        return s + "\n";
    }
    yaml::dump(v)
}

fn keep_diff_lines(out: &str, separate_hunks: bool) -> String {
    let mut keep = Vec::new();
    let mut seen_hunk = false;
    for ln in out.trim_end_matches('\n').split('\n') {
        if ln.starts_with("---") || ln.starts_with("+++") {
            continue;
        }
        if ln.starts_with("@@") {
            if separate_hunks && seen_hunk {
                keep.push("⋮");
            }
            seen_hunk = true;
            continue;
        }
        keep.push(ln);
    }
    keep.join("\n")
}

/// A unified diff with 2 lines of context, hunks separated by "⋮".
fn context_diff(before: &str, after: &str) -> String {
    let out = unified_diff(&split_lines(before), &split_lines(after), 2);
    keep_diff_lines(&out, true)
}

/// A unified line diff with 3 lines of context.
fn line_diff(before: &str, after: &str) -> String {
    let out = unified_diff(&split_lines(before), &split_lines(after), 3);
    keep_diff_lines(&out, false)
}

fn summary_line(attr: &str, kind: &str, total: usize, changed: usize) -> String {
    format!("~ {attr} · {kind} · {total} lines · {changed} changed (hidden to fit size limit)")
}

/// Everything [`diff`] needs for one attribute.
#[derive(Debug, Clone)]
pub struct Input {
    /// Resource type.
    pub resource_type: String,
    /// Attribute name.
    pub attr: String,
    /// Value before.
    pub before: Value,
    /// Value after.
    pub after: Value,
    /// The whole attribute is sensitive.
    pub sensitive: bool,
    /// Sensitivity tree before.
    pub before_sensitive: Value,
    /// Sensitivity tree after.
    pub after_sensitive: Value,
    /// Known after apply.
    pub unknown: bool,
    /// "", auto, structural, json, yaml, line, summary or hide.
    pub force_differ: String,
    /// Line cap for rich variants; 0 is none.
    pub max_lines: i64,
    /// Skip type sniffing for strings.
    pub no_detect: bool,
    /// Only sensitivity changed.
    pub sensitivity_only: bool,
}

/// Builds the field for one attribute.
pub fn diff(in_: &Input) -> Field {
    let mut f = diff_internal(in_);
    f.sensitivity_only = in_.sensitivity_only;
    f
}

fn diff_internal(in_: &Input) -> Field {
    if in_.unknown {
        return leaf_field(&in_.attr, LeafOp::Change, "(known after apply)");
    }
    if in_.sensitive {
        return leaf_field(&in_.attr, LeafOp::Change, "(sensitive value)");
    }
    let bs = in_.before.as_str();
    let as_ = in_.after.as_str();
    let structured = is_structured(&in_.before, &in_.after);

    if matches!(in_.before, Value::Null) && !matches!(in_.after, Value::Null) && !structured {
        let json_or_yaml =
            as_.is_some_and(|a| matches!(detect(a), ValueType::Json | ValueType::Yaml));
        let has_override = !in_.force_differ.is_empty() && in_.force_differ != "auto";
        if !json_or_yaml && !has_override {
            return scalar_leaf(&in_.attr, LeafOp::Add, String::new(), scalar(&in_.after));
        }
    }
    if matches!(in_.after, Value::Null) && !matches!(in_.before, Value::Null) && !structured {
        return scalar_leaf(
            &in_.attr,
            LeafOp::Remove,
            scalar(&in_.before),
            String::new(),
        );
    }

    match in_.force_differ.as_str() {
        "hide" => return block_field(&in_.attr, vec![Variant::new(Level::Hidden, String::new())]),
        "summary" => return block_field(&in_.attr, ladder_from(&in_.attr, Level::Summary, in_)),
        _ => {}
    }

    if bs.is_none() && as_.is_none() && structured {
        return structural(in_);
    }
    if !structured && bs.is_none() && as_.is_none() {
        return scalar_leaf(
            &in_.attr,
            LeafOp::Change,
            scalar(&in_.before),
            scalar(&in_.after),
        );
    }

    let (bs, as_) = (bs.unwrap_or(""), as_.unwrap_or(""));
    let mut kind = in_.force_differ.as_str();
    if kind.is_empty() || kind == "auto" {
        if in_.no_detect {
            kind = "line";
        } else {
            match detect(first_non_empty(bs, as_)) {
                ValueType::Json | ValueType::Yaml => return structural(in_),
                ValueType::Base64 => {
                    return block_field(&in_.attr, ladder_from(&in_.attr, Level::Summary, in_));
                }
                ValueType::Plain => kind = "line",
            }
        }
    }
    match kind {
        "structural" | "json" | "yaml" => structural(in_),
        _ => {
            if !bs.contains('\n') && !as_.contains('\n') && bs.len() < 60 && as_.len() < 60 {
                return scalar_leaf(
                    &in_.attr,
                    LeafOp::Change,
                    gofmt::quote(bs),
                    gofmt::quote(as_),
                );
            }
            block_field(&in_.attr, ladder_from(&in_.attr, Level::LineDiff, in_))
        }
    }
}

fn leaf_field(attr: &str, op: LeafOp, marker: &str) -> Field {
    Field {
        name: attr.to_string(),
        leaves: vec![Leaf {
            op,
            path: attr.to_string(),
            old: String::new(),
            new: String::new(),
            inline: marker.to_string(),
        }],
        ..Field::default()
    }
}

fn scalar_leaf(attr: &str, op: LeafOp, old: String, new: String) -> Field {
    Field {
        name: attr.to_string(),
        leaves: vec![Leaf {
            op,
            path: attr.to_string(),
            old,
            new,
            inline: String::new(),
        }],
        ..Field::default()
    }
}

fn block_field(attr: &str, variants: Vec<Variant>) -> Field {
    Field {
        name: attr.to_string(),
        variants,
        ..Field::default()
    }
}

fn structural(in_: &Input) -> Field {
    let kind = structured_kind(in_);
    let bv = redact_sensitive(&parse_structured(&in_.before), &in_.before_sensitive);
    let av = redact_sensitive(&parse_structured(&in_.after), &in_.after_sensitive);
    let before = canonical(&bv, kind);
    let after = canonical(&av, kind);
    let rich = context_diff(&before, &after);
    let (total, changed) = magnitude(&before, &after);
    let mut variants = Vec::new();
    if in_.max_lines == 0 || line_count(&rich) as i64 <= in_.max_lines {
        variants.push(Variant::new(Level::Structural, rich));
    }
    variants.push(Variant::new(
        Level::Summary,
        format!("  {}", summary_line(&in_.attr, kind, total, changed)),
    ));
    variants.push(Variant::new(Level::Hidden, String::new()));
    Field {
        name: in_.attr.clone(),
        kind: kind.to_string(),
        variants,
        ..Field::default()
    }
}

const SENSITIVE_MARKER: &str = "(sensitive value)";

/// Replaces every leaf the sensitivity tree marks with the sensitive marker.
fn redact_sensitive(v: &Value, s: &Value) -> Value {
    match s {
        Value::Bool(true) => Value::Str(SENSITIVE_MARKER.to_string()),
        Value::Map(sm) => match v {
            Value::Map(vm) => Value::Map(
                vm.iter()
                    .map(|(k, val)| {
                        (
                            k.clone(),
                            redact_sensitive(val, sm.get(k).unwrap_or(&Value::Null)),
                        )
                    })
                    .collect(),
            ),
            _ => v.clone(),
        },
        Value::Seq(sa) => match v {
            Value::Seq(va) => Value::Seq(
                va.iter()
                    .enumerate()
                    .map(|(i, val)| redact_sensitive(val, sa.get(i).unwrap_or(&Value::Null)))
                    .collect(),
            ),
            _ => v.clone(),
        },
        _ => v.clone(),
    }
}

fn ladder_from(attr: &str, top: Level, in_: &Input) -> Vec<Variant> {
    let bs = first_str(&in_.before);
    let as_ = first_str(&in_.after);
    if top == Level::Summary {
        let (total, changed) = magnitude(bs, as_);
        return vec![
            Variant::new(
                Level::Summary,
                format!(
                    "  {}",
                    summary_line(attr, summary_kind(bs, as_), total, changed)
                ),
            ),
            Variant::new(Level::Hidden, String::new()),
        ];
    }
    let rich = line_diff(bs, as_);
    let (mut total, mut changed) = magnitude(bs, as_);
    if total == 0 && !rich.is_empty() {
        let n = line_count(&rich);
        (total, changed) = (n, n);
    }
    let rich_content = indent(&rich);
    let mut variants = Vec::new();
    if !(in_.max_lines > 0 && line_count(&rich_content) as i64 > in_.max_lines) {
        variants.push(Variant::new(Level::LineDiff, rich_content));
    }
    variants.push(Variant::new(
        Level::Summary,
        format!("  {}", summary_line(attr, "text", total, changed)),
    ));
    variants.push(Variant::new(Level::Hidden, String::new()));
    variants
}

/// Native structured values as they are; a string parsed as JSON, else YAML.
fn parse_structured(native: &Value) -> Value {
    if native.is_structured() {
        return native.clone();
    }
    let s = first_str(native);
    if s.is_empty() {
        return native.clone();
    }
    if crate::gojson::check_valid(s.as_bytes()).is_ok()
        && let Some(v) = goval::parse_json(s)
    {
        return v;
    }
    match yaml::load(s) {
        Ok(v) => v,
        Err(_) => native.clone(),
    }
}

fn magnitude(before: &str, after: &str) -> (usize, usize) {
    let mut total = line_count(after);
    if total == 0 {
        total = line_count(before);
    }
    let out = unified_diff(&split_lines(before), &split_lines(after), 0);
    let changed = out
        .split('\n')
        .filter(|ln| {
            (ln.starts_with('+') || ln.starts_with('-'))
                && !ln.starts_with("+++")
                && !ln.starts_with("---")
        })
        .count();
    (total, changed)
}

fn summary_kind(bs: &str, as_: &str) -> &'static str {
    match detect(first_non_empty(bs, as_)) {
        ValueType::Json => "json",
        ValueType::Yaml => "yaml",
        ValueType::Base64 => "base64",
        ValueType::Plain => "text",
    }
}

fn indent(s: &str) -> String {
    if s.is_empty() {
        return String::new();
    }
    s.split('\n')
        .map(|l| format!("  {l}"))
        .collect::<Vec<_>>()
        .join("\n")
}

fn line_count(s: &str) -> usize {
    if s.is_empty() {
        return 0;
    }
    s.trim_end_matches('\n').matches('\n').count() + 1
}

#[cfg(test)]
mod tests;
