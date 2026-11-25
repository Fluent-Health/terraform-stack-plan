//! The render-related part of the HCL policy file (`.tfstackplan.hcl`, `internal/config`):
//! classification rules and presets, diff overrides, and links.
//!
//! Blocks for other subcommands are recognised so a shared policy file loads, and their labels
//! and attribute shapes are checked like Go's decoder does. `server`, `serve`, `ui` and
//! `env_uniqueness` are accepted without checking their contents.
//!
//! Errors read like Go's: decode problems come out as hcl diagnostics
//! (`file:3,5-9: Unsupported argument; An argument named "x" is not expected here.`). HCL
//! syntax errors carry the parser's own message, which differs from Go's.

use std::collections::HashMap;

use hcl::edit::Span;
use hcl::edit::structure::{Block, BlockLabel, Body, Structure};
use hcl::eval::Evaluate;

use crate::classify::{self, Derivation, Rule};
use crate::gofmt::{self, quote};
use crate::goregex::GoRegex;
use crate::model::Category;

/// The parsed policy file, render-related parts only.
#[derive(Debug, Clone)]
pub struct Config {
    /// `None` when there is no classification block.
    pub classification: Option<Classification>,
    /// Diff defaults and overrides.
    pub diff: DiffConfig,
    /// `None` when there is no links block.
    pub links: Option<LinksConfig>,
}

impl Default for Config {
    fn default() -> Self {
        Config {
            classification: None,
            diff: DiffConfig {
                detect: true,
                ..DiffConfig::default()
            },
            links: None,
        }
    }
}

/// Resolved, ordered rules and the fallback category.
#[derive(Debug, Clone)]
pub struct Classification {
    /// Badge for a stack no rule matched.
    pub default: Category,
    /// Rules in declaration order.
    pub rules: Vec<Rule>,
}

/// Diff defaults and per-attribute overrides.
#[derive(Debug, Clone, Default)]
pub struct DiffConfig {
    /// Sniff string values for JSON/YAML.
    pub detect: bool,
    /// Line cap for rich variants; 0 is none.
    pub max_attribute_lines: i64,
    /// Overrides in declaration order.
    pub overrides: Vec<DiffOverride>,
}

/// Forces a differ for a resource type and attribute.
#[derive(Debug, Clone)]
pub struct DiffOverride {
    /// Resource types; `None` for any.
    pub type_pattern: Option<GoRegex>,
    /// Attribute glob (`path.Match` syntax); empty for any.
    pub attribute: String,
    /// Differ name.
    pub differ: String,
}

impl DiffConfig {
    /// The differ for a resource type and attribute ("" means auto): the first override that
    /// matches.
    pub fn resolve(&self, resource_type: &str, attr: &str) -> String {
        for ov in &self.overrides {
            if ov.type_pattern.as_ref().is_some_and(|re| !re.is_match(resource_type)) {
                continue;
            }
            if !ov.attribute.is_empty() && !glob_match(&ov.attribute, attr) {
                continue;
            }
            return ov.differ.clone();
        }
        String::new()
    }
}

/// URL templates for header, stack and resource links.
#[derive(Debug, Clone, Default)]
pub struct LinksConfig {
    /// Resource link template.
    pub resource: String,
    /// Stack link template.
    pub stack: String,
    /// Header links.
    pub header: Vec<HeaderLink>,
}

/// One templated header link.
#[derive(Debug, Clone)]
pub struct HeaderLink {
    /// Label template.
    pub label: String,
    /// URL template.
    pub url: String,
}

// ---------------------------------------------------------------------------------------------
// Diagnostics in hcl's format.

struct Src<'a> {
    file: &'a str,
    text: &'a str,
}

impl Src<'_> {
    /// hcl's 1-based line and column (columns count characters) of a byte offset.
    fn pos(&self, offset: usize) -> (usize, usize) {
        let before = &self.text[..offset.min(self.text.len())];
        let line = before.matches('\n').count() + 1;
        let line_start = before.rfind('\n').map_or(0, |i| i + 1);
        (line, before[line_start..].chars().count() + 1)
    }

    fn range(&self, r: &std::ops::Range<usize>) -> String {
        let (l1, c1) = self.pos(r.start);
        let (l2, c2) = self.pos(r.end);
        if l1 == l2 {
            format!("{}:{l1},{c1}-{c2}", self.file)
        } else {
            format!("{}:{l1},{c1}-{l2},{c2}", self.file)
        }
    }

    fn diag(&self, r: &std::ops::Range<usize>, summary: &str, detail: &str) -> String {
        format!("{}: {summary}; {detail}", self.range(r))
    }

    /// Where a block's `{` is: the first brace after its type and labels.
    fn open_brace(&self, b: &Block) -> std::ops::Range<usize> {
        let after = b
            .labels
            .last()
            .and_then(Span::span)
            .or_else(|| b.ident.span())
            .map_or(0, |r| r.end);
        let at = self.text[after.min(self.text.len())..]
            .find('{')
            .map_or(after, |i| after + i);
        at..at + 1
    }
}

/// hcl's `Diagnostics.Error()`.
fn diags_error(diags: &[String]) -> String {
    match diags.len() {
        0 => "no diagnostics".to_string(),
        1 => diags[0].clone(),
        n => format!("{}, and {} other diagnostic(s)", diags[0], n - 1),
    }
}

// ---------------------------------------------------------------------------------------------
// A small gohcl: decode a body against a schema.

#[derive(Clone, Copy, PartialEq)]
enum Ty {
    Str,
    Int,
    Bool,
    Float,
    StrList,
}

struct AttrSpec {
    name: &'static str,
    ty: Ty,
    required: bool,
}

const fn opt(name: &'static str, ty: Ty) -> AttrSpec {
    AttrSpec {
        name,
        ty,
        required: false,
    }
}

const fn req(name: &'static str, ty: Ty) -> AttrSpec {
    AttrSpec {
        name,
        ty,
        required: true,
    }
}

struct BlockSpec {
    ty: &'static str,
    labels: &'static [&'static str],
}

#[derive(Debug, Clone)]
enum Val {
    Str(String),
    Int(i64),
    Bool(bool),
    Float,
    StrList(Vec<String>),
}

#[derive(Default)]
struct Decoded<'a> {
    vals: HashMap<&'static str, Val>,
    blocks: Vec<&'a Block>,
}

impl Decoded<'_> {
    fn str(&self, name: &str) -> String {
        match self.vals.get(name) {
            Some(Val::Str(s)) => s.clone(),
            _ => String::new(),
        }
    }

    fn int(&self, name: &str) -> i64 {
        match self.vals.get(name) {
            Some(Val::Int(i)) => *i,
            _ => 0,
        }
    }

    fn list(&self, name: &str) -> Vec<String> {
        match self.vals.get(name) {
            Some(Val::StrList(v)) => v.clone(),
            _ => Vec::new(),
        }
    }
}

fn attributes(body: &Body) -> impl Iterator<Item = &hcl::edit::structure::Attribute> {
    body.iter().filter_map(|s| match s {
        Structure::Attribute(a) => Some(a),
        Structure::Block(_) => None,
    })
}

fn blocks(body: &Body) -> impl Iterator<Item = &Block> {
    body.iter().filter_map(|s| match s {
        Structure::Block(b) => Some(b),
        Structure::Attribute(_) => None,
    })
}

fn label_str(l: &BlockLabel) -> &str {
    l.as_str()
}

fn levenshtein(a: &str, b: &str) -> usize {
    let a: Vec<char> = a.chars().collect();
    let b: Vec<char> = b.chars().collect();
    let mut prev: Vec<usize> = (0..=b.len()).collect();
    for (i, ca) in a.iter().enumerate() {
        let mut cur = vec![i + 1];
        for (j, cb) in b.iter().enumerate() {
            let cost = usize::from(ca != cb);
            cur.push((prev[j] + cost).min(prev[j + 1] + 1).min(cur[j] + 1));
        }
        prev = cur;
    }
    prev[b.len()]
}

fn name_suggestion<'a>(given: &str, suggestions: impl IntoIterator<Item = &'a str>) -> Option<&'a str> {
    suggestions.into_iter().find(|s| levenshtein(given, s) < 3)
}

/// Evaluates an expression with no variables or functions, as hcl does with a nil context.
fn eval(expr: &hcl::edit::expr::Expression) -> Result<hcl::Value, String> {
    let e: hcl::Expression = expr.clone().into();
    e.evaluate(&hcl::eval::Context::new()).map_err(|e| e.to_string())
}

fn number_text(n: &hcl::Number) -> String {
    n.as_f64().map(gofmt::format_f).unwrap_or_default()
}

/// cty's conversion of a value to the target type, with its error text.
fn convert(v: &hcl::Value, ty: Ty) -> Result<Val, String> {
    use hcl::Value as V;
    let as_string = |v: &V| -> Option<String> {
        match v {
            V::String(s) => Some(s.clone()),
            V::Number(n) => Some(number_text(n)),
            V::Bool(b) => Some(b.to_string()),
            _ => None,
        }
    };
    if matches!(v, V::Null) {
        return Err("null value is not allowed".to_string());
    }
    match ty {
        Ty::Str => as_string(v).map(Val::Str).ok_or_else(|| "string required".to_string()),
        Ty::Bool => match v {
            V::Bool(b) => Ok(Val::Bool(*b)),
            V::String(s) if s == "true" => Ok(Val::Bool(true)),
            V::String(s) if s == "false" => Ok(Val::Bool(false)),
            V::String(_) => Err("a bool is required".to_string()),
            _ => Err("bool required".to_string()),
        },
        Ty::Int | Ty::Float => {
            let f = match v {
                V::Number(n) => n.as_f64(),
                V::String(s) => s.parse::<f64>().ok(),
                _ => return Err("number required".to_string()),
            }
            .ok_or_else(|| "a number is required".to_string())?;
            if ty == Ty::Float {
                return Ok(Val::Float);
            }
            match gofmt::as_exact_i64(f) {
                Some(i) => Ok(Val::Int(i)),
                None => Err(
                    "value must be a whole number, between -9223372036854775808 and 9223372036854775807".to_string(),
                ),
            }
        }
        Ty::StrList => match v {
            V::Array(items) => {
                let mut out = Vec::new();
                for (i, item) in items.iter().enumerate() {
                    match as_string(item) {
                        Some(s) => out.push(s),
                        None => return Err(format!("element {i}: string required")),
                    }
                }
                Ok(Val::StrList(out))
            }
            _ => Err("list of string required".to_string()),
        },
    }
}

/// Decodes one expression into a type, as `gohcl.DecodeExpression` does. Returns the
/// diagnostic text on failure.
fn decode_expr(src: &Src<'_>, expr: &hcl::edit::expr::Expression, ty: Ty) -> Result<Val, String> {
    let span = expr.span().unwrap_or(0..0);
    let v = eval(expr).map_err(|e| src.diag(&span, "Invalid expression", &e))?;
    convert(&v, ty).map_err(|e| {
        let start = if matches!(expr, hcl::edit::expr::Expression::Array(_)) {
            span.start..span.start + 1
        } else {
            span.clone()
        };
        src.diag(&start, "Unsuitable value type", &format!("Unsuitable value: {e}"))
    })
}

/// `gohcl.DecodeBody` against a schema: hcl's content checks, then each attribute's value.
/// Wanted blocks come back for the caller to decode; their diagnostics are appended to the same
/// list, as gohcl does for nested blocks.
fn decode_body<'a>(
    src: &Src<'_>,
    body: &'a Body,
    open_brace: &std::ops::Range<usize>,
    attrs: &[AttrSpec],
    want_blocks: &[BlockSpec],
    diags: &mut Vec<String>,
) -> Decoded<'a> {
    let mut out = Decoded::default();
    let present: Vec<&hcl::edit::structure::Attribute> = attributes(body).collect();
    let find = |name: &str| present.iter().rev().find(|a| a.key.as_str() == name).copied();
    for spec in attrs {
        if spec.required && find(spec.name).is_none() {
            diags.push(src.diag(
                &(open_brace.start..open_brace.start),
                "Missing required argument",
                &format!("The argument {} is required, but no definition was found.", quote(spec.name)),
            ));
        }
    }
    for b in blocks(body) {
        let Some(spec) = want_blocks.iter().find(|s| s.ty == b.ident.as_str()) else {
            continue;
        };
        let name = spec.ty;
        if b.labels.len() > spec.labels.len() {
            let span = b.labels[spec.labels.len()].span().unwrap_or(0..0);
            let detail = if spec.labels.is_empty() {
                format!("No labels are expected for {name} blocks.")
            } else {
                format!(
                    "Only {} labels ({}) are expected for {name} blocks.",
                    spec.labels.len(),
                    spec.labels.join(", ")
                )
            };
            diags.push(src.diag(&span, &format!("Extraneous label for {name}"), &detail));
            continue;
        }
        if b.labels.len() < spec.labels.len() {
            diags.push(src.diag(
                &src.open_brace(b),
                &format!("Missing {} for {name}", spec.labels[b.labels.len()]),
                &format!(
                    "All {name} blocks must have {} labels ({}).",
                    spec.labels.len(),
                    spec.labels.join(", ")
                ),
            ));
            continue;
        }
        out.blocks.push(b);
    }
    for a in &present {
        let name = a.key.as_str();
        if attrs.iter().any(|s| s.name == name) {
            continue;
        }
        let suggestion = match name_suggestion(name, attrs.iter().map(|s| s.name)) {
            Some(s) => format!(" Did you mean {}?", quote(s)),
            None if want_blocks.iter().any(|b| b.ty == name) => {
                format!(" Did you mean to define a block of type {}?", quote(name))
            }
            None => String::new(),
        };
        diags.push(src.diag(
            &a.key.span().unwrap_or(0..0),
            "Unsupported argument",
            &format!("An argument named {} is not expected here.{suggestion}", quote(name)),
        ));
    }
    for b in blocks(body) {
        let ty = b.ident.as_str();
        if want_blocks.iter().any(|s| s.ty == ty) {
            continue;
        }
        let suggestion = match name_suggestion(ty, want_blocks.iter().map(|s| s.ty)) {
            Some(s) => format!(" Did you mean {}?", quote(s)),
            None if attrs.iter().any(|s| s.name == ty) => format!(
                " Did you mean to define argument {}? If so, use the equals sign to assign it a value.",
                quote(ty)
            ),
            None => String::new(),
        };
        diags.push(src.diag(
            &b.ident.span().unwrap_or(0..0),
            "Unsupported block type",
            &format!("Blocks of type {} are not expected here.{suggestion}", quote(ty)),
        ));
    }
    for spec in attrs {
        if let Some(a) = find(spec.name) {
            match decode_expr(src, &a.value, spec.ty) {
                Ok(v) => {
                    out.vals.insert(spec.name, v);
                }
                Err(d) => diags.push(d),
            }
        }
    }
    out
}

fn checked<'a>(
    src: &Src<'_>,
    body: &'a Body,
    open_brace: &std::ops::Range<usize>,
    attrs: &[AttrSpec],
    want_blocks: &[BlockSpec],
) -> Result<Decoded<'a>, String> {
    let mut diags = Vec::new();
    let d = decode_body(src, body, open_brace, attrs, want_blocks, &mut diags);
    if diags.is_empty() {
        Ok(d)
    } else {
        Err(diags_error(&diags))
    }
}

/// A top-level attribute decoded on its own (`body.Attributes["x"]` then DecodeExpression).
fn attr_value(src: &Src<'_>, body: &Body, name: &str, ty: Ty) -> Option<Result<Val, String>> {
    attributes(body)
        .filter(|a| a.key.as_str() == name)
        .last()
        .map(|a| decode_expr(src, &a.value, ty))
}

// ---------------------------------------------------------------------------------------------
// Blocks.

const DERIVE_ATTRS: &[AttrSpec] = &[
    opt("resource_type_pattern", Ty::Str),
    req("from_attribute", Ty::Str),
    req("pattern", Ty::Str),
];

struct DeriveBlock {
    attribute: String,
    type_pattern: String,
    from_attribute: String,
    pattern: String,
}

fn decode_derives(src: &Src<'_>, blocks: &[&Block], diags: &mut Vec<String>) -> Vec<DeriveBlock> {
    let mut out = Vec::new();
    for b in blocks {
        let d = decode_body(src, &b.body, &src.open_brace(b), DERIVE_ATTRS, &[], diags);
        out.push(DeriveBlock {
            attribute: b.labels.first().map(label_str).unwrap_or("").to_string(),
            type_pattern: d.str("resource_type_pattern"),
            from_attribute: d.str("from_attribute"),
            pattern: d.str("pattern"),
        });
    }
    out
}

fn build_derivations(label: &str, blocks: Vec<DeriveBlock>) -> Result<Vec<Derivation>, String> {
    let mut out = Vec::new();
    for b in blocks {
        if b.attribute.is_empty() {
            return Err(format!("{label}: derive block needs an attribute label"));
        }
        let a = quote(&b.attribute);
        if b.from_attribute.is_empty() {
            return Err(format!("{label}: derive {a}: from_attribute is required"));
        }
        if b.pattern.is_empty() {
            return Err(format!("{label}: derive {a}: pattern is required"));
        }
        let pattern = GoRegex::new(&b.pattern).map_err(|e| format!("{label}: derive {a}: bad pattern: {e}"))?;
        if pattern.num_subexp() == 0 {
            return Err(format!("{label}: derive {a}: pattern needs a capture group"));
        }
        let type_pattern = if b.type_pattern.is_empty() {
            None
        } else {
            Some(
                GoRegex::new(&b.type_pattern)
                    .map_err(|e| format!("{label}: derive {a}: bad resource_type_pattern: {e}"))?,
            )
        };
        out.push(Derivation {
            attribute: b.attribute,
            type_pattern,
            from_attribute: b.from_attribute,
            pattern,
        });
    }
    Ok(out)
}

const DERIVE_BLOCK: BlockSpec = BlockSpec {
    ty: "derive",
    labels: &["attribute"],
};

fn decode_classification(src: &Src<'_>, blk: &Block) -> Result<Classification, String> {
    let mut cl = Classification {
        default: Category {
            name: "safe".to_string(),
            ..Category::default()
        },
        rules: Vec::new(),
    };
    if let Some(v) = attr_value(src, &blk.body, "default", Ty::Str) {
        let Val::Str(s) = v.map_err(|d| format!("default: {d}"))? else {
            unreachable!()
        };
        cl.default = Category {
            name: s,
            ..Category::default()
        };
    }
    for b in blocks(&blk.body) {
        match b.ident.as_str() {
            "default" => {
                let d = checked(src, &b.body, &src.open_brace(b), &[req("name", Ty::Str), opt("icon", Ty::Str)], &[])
                    .map_err(|e| format!("default block: {e}"))?;
                cl.default = Category {
                    name: d.str("name"),
                    icon: d.str("icon"),
                    attributes: None,
                };
            }
            "preset" => {
                if b.labels.len() != 1 {
                    return Err("preset block needs exactly one name label".to_string());
                }
                let name = label_str(&b.labels[0]);
                let mut diags = Vec::new();
                let d = decode_body(
                    src,
                    &b.body,
                    &src.open_brace(b),
                    &[opt("icon", Ty::Str), opt("emit_attributes", Ty::StrList)],
                    &[DERIVE_BLOCK],
                    &mut diags,
                );
                let derives = decode_derives(src, &d.blocks, &mut diags);
                if !diags.is_empty() {
                    return Err(format!("preset {}: {}", quote(name), diags_error(&diags)));
                }
                let mut rule = classify::preset_rule(name, &d.str("icon"), d.list("emit_attributes")).ok_or_else(|| {
                    let names: Vec<&str> = classify::PRESET_NAMES.to_vec();
                    format!("unknown preset {} (available: [{}])", quote(name), names.join(" "))
                })?;
                rule.derivations = build_derivations(&format!("preset {}", quote(name)), derives)?;
                cl.rules.push(rule);
            }
            "rule" => {
                if b.labels.len() != 1 {
                    return Err("rule block needs exactly one name label".to_string());
                }
                let name = label_str(&b.labels[0]);
                let mut diags = Vec::new();
                let d = decode_body(
                    src,
                    &b.body,
                    &src.open_brace(b),
                    &[
                        opt("icon", Ty::Str),
                        opt("resource_type_pattern", Ty::Str),
                        opt("actions", Ty::StrList),
                        opt("min_count", Ty::Int),
                        opt("emit_attributes", Ty::StrList),
                    ],
                    &[DERIVE_BLOCK],
                    &mut diags,
                );
                let derives = decode_derives(src, &d.blocks, &mut diags);
                if !diags.is_empty() {
                    return Err(format!("rule {}: {}", quote(name), diags_error(&diags)));
                }
                let type_pattern = match d.str("resource_type_pattern") {
                    p if p.is_empty() => None,
                    p => Some(
                        GoRegex::new(&p)
                            .map_err(|e| format!("rule {}: bad resource_type_pattern: {e}", quote(name)))?,
                    ),
                };
                let mut rule = Rule {
                    name: name.to_string(),
                    icon: d.str("icon"),
                    type_pattern,
                    actions: d.list("actions"),
                    min_count: d.int("min_count"),
                    emit_attributes: d.list("emit_attributes"),
                    derivations: Vec::new(),
                };
                rule.derivations = build_derivations(&format!("rule {}", quote(name)), derives)?;
                cl.rules.push(rule);
            }
            other => return Err(format!("classification: unknown block {}", quote(other))),
        }
    }
    Ok(cl)
}

fn decode_diff(src: &Src<'_>, blk: &Block) -> Result<DiffConfig, String> {
    let mut d = DiffConfig {
        detect: true,
        ..DiffConfig::default()
    };
    if let Some(v) = attr_value(src, &blk.body, "detect", Ty::Bool)
        && let Val::Bool(b) = v.map_err(|e| format!("diff.detect: {e}"))?
    {
        d.detect = b;
    }
    if let Some(v) = attr_value(src, &blk.body, "max_attribute_lines", Ty::Int)
        && let Val::Int(i) = v.map_err(|e| format!("diff.max_attribute_lines: {e}"))?
    {
        d.max_attribute_lines = i;
    }
    for b in blocks(&blk.body) {
        if b.ident.as_str() != "rule" {
            return Err(format!("diff: unknown block {}", quote(b.ident.as_str())));
        }
        let r = checked(
            src,
            &b.body,
            &src.open_brace(b),
            &[
                opt("resource_type_pattern", Ty::Str),
                opt("attribute", Ty::Str),
                opt("differ", Ty::Str),
            ],
            &[],
        )
        .map_err(|e| format!("diff rule: {e}"))?;
        let type_pattern = match r.str("resource_type_pattern") {
            p if p.is_empty() => None,
            p => Some(GoRegex::new(&p).map_err(|e| format!("diff rule: bad resource_type_pattern: {e}"))?),
        };
        d.overrides.push(DiffOverride {
            type_pattern,
            attribute: r.str("attribute"),
            differ: r.str("differ"),
        });
    }
    Ok(d)
}

fn decode_links(src: &Src<'_>, blk: &Block) -> Result<LinksConfig, String> {
    let mut lc = LinksConfig::default();
    for (name, target) in [("resource", &mut lc.resource), ("stack", &mut lc.stack)] {
        if let Some(v) = attr_value(src, &blk.body, name, Ty::Str)
            && let Val::Str(s) = v.map_err(|e| format!("links.{name}: {e}"))?
        {
            *target = s;
        }
    }
    for b in blocks(&blk.body) {
        if b.ident.as_str() != "header" {
            return Err(format!("links: unknown block {}", quote(b.ident.as_str())));
        }
        let h = checked(src, &b.body, &src.open_brace(b), &[req("label", Ty::Str), req("url", Ty::Str)], &[])
            .map_err(|e| format!("links.header: {e}"))?;
        lc.header.push(HeaderLink {
            label: h.str("label"),
            url: h.str("url"),
        });
    }
    Ok(lc)
}

const PHASES: &[&str] = &[
    "warming",
    "linting",
    "initializing",
    "planning",
    "applying",
    "testing",
    "verifying",
    "image",
    "claim",
    "moves",
    "classify",
    "report",
];

fn check_progress(src: &Src<'_>, blk: &Block) -> Result<(), String> {
    for op in blocks(&blk.body) {
        let kind = op.ident.as_str();
        if kind != "plan" && kind != "apply" {
            return Err(format!("progress: unknown block {} (want plan|apply)", quote(kind)));
        }
        for b in blocks(&op.body) {
            if b.ident.as_str() != "phase" {
                return Err(format!(
                    "progress.{kind}: unknown block {} (want phase)",
                    quote(b.ident.as_str())
                ));
            }
            if b.labels.len() != 1 {
                return Err(format!("progress.{kind}.phase: expected one label (the phase name)"));
            }
            let phase = label_str(&b.labels[0]);
            if !PHASES.contains(&phase) {
                return Err(format!("progress.{kind}: unknown phase {}", quote(phase)));
            }
            if let Some(Err(e)) = attr_value(src, &b.body, "weight", Ty::Float) {
                return Err(format!("progress.{kind}.phase {} weight: {e}", quote(phase)));
            }
        }
    }
    Ok(())
}

/// Parses and checks a policy file. `path` is used in messages as given.
pub fn load(path: &str, text: &str) -> Result<Config, String> {
    let src = Src { file: path, text };
    let body = hcl::edit::parser::parse_body(text).map_err(|e| format!("parse {path}: {e}"))?;
    let mut cfg = Config::default();
    for blk in blocks(&body) {
        match blk.ident.as_str() {
            "classification" => cfg.classification = Some(decode_classification(&src, blk)?),
            "diff" => cfg.diff = decode_diff(&src, blk)?,
            "links" => cfg.links = Some(decode_links(&src, blk)?),
            "class" => {
                if blk.labels.len() != 1 {
                    return Err("class block needs exactly one name label".to_string());
                }
                checked(
                    &src,
                    &blk.body,
                    &src.open_brace(blk),
                    &[
                        opt("backend", Ty::Str),
                        opt("entitlement", Ty::Str),
                        opt("entitlement_scope", Ty::Str),
                        opt("required", Ty::Bool),
                    ],
                    &[],
                )
                .map_err(|e| format!("class {}: {e}", quote(label_str(&blk.labels[0]))))?;
            }
            "cache" => {
                checked(
                    &src,
                    &blk.body,
                    &src.open_brace(blk),
                    &[opt("bucket", Ty::Str), opt("prefix", Ty::Str), opt("version", Ty::Str)],
                    &[],
                )?;
            }
            "progress" => check_progress(&src, blk)?,
            "server" | "ui" | "serve" | "env_uniqueness" => {}
            other => return Err(format!("{path}: unknown top-level block {}", quote(other))),
        }
    }
    Ok(cfg)
}

/// Go's `path.Match` (`filepath.Match` on unix): `*`, `?`, `[...]` and `\` escapes, where `*`
/// and `?` do not match `/`. A malformed pattern matches nothing.
pub fn glob_match(pattern: &str, name: &str) -> bool {
    glob(pattern.as_bytes(), name.as_bytes()).unwrap_or(false)
}

fn glob(mut pattern: &[u8], mut name: &[u8]) -> Option<bool> {
    'pattern: while !pattern.is_empty() {
        let star = pattern[0] == b'*';
        while pattern.first() == Some(&b'*') {
            pattern = &pattern[1..];
        }
        let chunk_end = scan_chunk(pattern);
        let chunk = &pattern[..chunk_end];
        pattern = &pattern[chunk_end..];
        if star && chunk.is_empty() {
            return Some(!name.contains(&b'/'));
        }
        let (rest, ok) = match_chunk(chunk, name)?;
        if ok && (rest.is_empty() || !pattern.is_empty()) {
            name = rest;
            continue;
        }
        if star {
            let mut i = 0;
            while i < name.len() && name[i] != b'/' {
                let (rest, ok) = match_chunk(chunk, &name[i + 1..])?;
                if ok {
                    if pattern.is_empty() && !rest.is_empty() {
                        i += 1;
                        continue;
                    }
                    name = rest;
                    continue 'pattern;
                }
                i += 1;
            }
        }
        // Go checks the rest of the pattern for syntax errors before reporting no match.
        while !pattern.is_empty() {
            while pattern.first() == Some(&b'*') {
                pattern = &pattern[1..];
            }
            let end = scan_chunk(pattern);
            match_chunk(&pattern[..end], b"")?;
            pattern = &pattern[end..];
        }
        return Some(false);
    }
    Some(name.is_empty())
}

fn scan_chunk(pattern: &[u8]) -> usize {
    let mut in_range = false;
    let mut i = 0;
    while i < pattern.len() {
        match pattern[i] {
            b'\\' => {
                if i + 1 < pattern.len() {
                    i += 1;
                }
            }
            b'[' => in_range = true,
            b']' => in_range = false,
            b'*' if !in_range => break,
            _ => {}
        }
        i += 1;
    }
    i
}

/// `utf8.DecodeRune`: the rune and its width, U+FFFD and 1 for an invalid byte.
fn decode_rune(s: &[u8]) -> (char, usize) {
    let n = s.len().min(4);
    for w in 1..=n {
        if let Ok(t) = std::str::from_utf8(&s[..w])
            && let Some(c) = t.chars().next()
        {
            return (c, w);
        }
    }
    ('\u{FFFD}', 1)
}

/// Matches a star-free chunk at the start of `s`; `None` for a malformed pattern.
fn match_chunk<'a>(mut chunk: &[u8], mut s: &'a [u8]) -> Option<(&'a [u8], bool)> {
    let mut failed = false;
    while !chunk.is_empty() {
        if !failed && s.is_empty() {
            failed = true;
        }
        match chunk[0] {
            b'[' => {
                let mut r = '\0';
                if !failed {
                    let (c, w) = decode_rune(s);
                    r = c;
                    s = &s[w..];
                }
                chunk = &chunk[1..];
                let negated = chunk.first() == Some(&b'^');
                if negated {
                    chunk = &chunk[1..];
                }
                let mut matched = false;
                let mut nrange = 0;
                loop {
                    if chunk.first() == Some(&b']') && nrange > 0 {
                        chunk = &chunk[1..];
                        break;
                    }
                    let (lo, rest) = get_esc(chunk)?;
                    chunk = rest;
                    let mut hi = lo;
                    if chunk.first() == Some(&b'-') {
                        let (h, rest) = get_esc(&chunk[1..])?;
                        hi = h;
                        chunk = rest;
                    }
                    if lo <= r && r <= hi {
                        matched = true;
                    }
                    nrange += 1;
                }
                if matched == negated {
                    failed = true;
                }
            }
            b'?' => {
                if !failed {
                    if s[0] == b'/' {
                        failed = true;
                    }
                    let (_, w) = decode_rune(s);
                    s = &s[w..];
                }
                chunk = &chunk[1..];
            }
            b'\\' => {
                chunk = &chunk[1..];
                if chunk.is_empty() {
                    return None;
                }
                if !failed {
                    if chunk[0] != s[0] {
                        failed = true;
                    }
                    s = &s[1..];
                }
                chunk = &chunk[1..];
            }
            c => {
                if !failed {
                    if c != s[0] {
                        failed = true;
                    }
                    s = &s[1..];
                }
                chunk = &chunk[1..];
            }
        }
    }
    if failed {
        return Some((b"", false));
    }
    Some((s, true))
}

fn get_esc(chunk: &[u8]) -> Option<(char, &[u8])> {
    let mut chunk = chunk;
    if chunk.is_empty() || chunk[0] == b'-' || chunk[0] == b']' {
        return None;
    }
    if chunk[0] == b'\\' {
        chunk = &chunk[1..];
        if chunk.is_empty() {
            return None;
        }
    }
    let (r, w) = decode_rune(chunk);
    if r == '\u{FFFD}' && w == 1 {
        return None;
    }
    let rest = &chunk[w..];
    if rest.is_empty() {
        return None;
    }
    Some((r, rest))
}

#[cfg(test)]
mod tests;
