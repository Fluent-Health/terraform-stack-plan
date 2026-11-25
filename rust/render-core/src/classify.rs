//! Applies an ordered list of rules to a parsed stack and returns the matching categories
//! (`internal/classify`, with its built-in presets).

use std::collections::{BTreeMap, BTreeSet};
use std::sync::OnceLock;

use crate::gofmt;
use crate::goregex::GoRegex;
use crate::goval::Value;
use crate::model::Category;
use crate::moveset::Set;
use crate::plan::{RawChange, RawStack};

/// One classification rule.
#[derive(Debug, Clone, Default)]
pub struct Rule {
    /// Category name.
    pub name: String,
    /// Glyph.
    pub icon: String,
    /// Resource types to match; `None` matches any.
    pub type_pattern: Option<GoRegex>,
    /// Raw Terraform actions that must all be present; empty matches any.
    pub actions: Vec<String>,
    /// Minimum matching changes (treated as 1 below 1).
    pub min_count: i64,
    /// Attributes to extract from matched changes.
    pub emit_attributes: Vec<String>,
    /// Per-resource recovery of an absent emitted attribute.
    pub derivations: Vec<Derivation>,
}

/// Recovers an emitted attribute from another scalar attribute with a capture group.
#[derive(Debug, Clone)]
pub struct Derivation {
    /// The emitted attribute this fills, e.g. "project".
    pub attribute: String,
    /// Resource types it applies to; `None` for any.
    pub type_pattern: Option<GoRegex>,
    /// Source scalar attribute.
    pub from_attribute: String,
    /// Capture group "value", else group 1, yields the value.
    pub pattern: GoRegex,
}

/// The names of the built-in presets, for error messages.
pub const PRESET_NAMES: &[&str] = &["iam"];

fn iam_pattern() -> GoRegex {
    static RE: OnceLock<GoRegex> = OnceLock::new();
    RE.get_or_init(|| {
        GoRegex::new(r"(?:_iam_(?:policy|binding|member|audit_config)$)|^aws_iam_|^azurerm_role_(?:assignment|definition)$")
            .unwrap_or_else(|_| unreachable!())
    })
    .clone()
}

/// The built-in rule for a preset name, with an optional icon override.
pub fn preset_rule(name: &str, icon_override: &str, emit_attributes: Vec<String>) -> Option<Rule> {
    match name {
        "iam" => Some(Rule {
            name: "iam".to_string(),
            icon: if icon_override.is_empty() {
                "🔐".to_string()
            } else {
                icon_override.to_string()
            },
            type_pattern: Some(iam_pattern()),
            min_count: 1,
            emit_attributes,
            ..Rule::default()
        }),
        _ => None,
    }
}

/// A category for every rule that matches enough changes, in rule order.
pub fn classify(s: &RawStack, rules: &[Rule], move_targets: &Set) -> Vec<Category> {
    let mut cats = Vec::new();
    for r in rules {
        let min = r.min_count.max(1);
        let matched: Vec<&RawChange> = s
            .changes
            .iter()
            .filter(|c| {
                c.action.mutates()
                    && !c.sensitivity_only
                    && !move_targets.covers(&c.address)
                    && !(!c.previous_address.is_empty() && move_targets.covers(&c.previous_address))
            })
            .filter(|c| rule_matches(r, c))
            .collect();
        if matched.len() as i64 >= min {
            let mut attrs = extract(&matched, &r.emit_attributes, &r.derivations);
            if r.emit_attributes.iter().any(|n| n == "project")
                && attrs.as_ref().and_then(|a| a.get("project")).is_none_or(Vec::is_empty)
                && let Some(sp) = derive_stack_project(&s.changes)
            {
                attrs
                    .get_or_insert_with(BTreeMap::new)
                    .insert("project".to_string(), vec![sp]);
            }
            cats.push(Category {
                name: r.name.clone(),
                icon: r.icon.clone(),
                attributes: attrs,
            });
        }
    }
    cats
}

/// Unions categories across stacks, one per rule (in rule order) that any stack matched.
pub fn summarize(per_stack: &[Vec<Category>], rules: &[Rule]) -> Vec<Category> {
    let mut agg: BTreeMap<&str, Category> = BTreeMap::new();
    for cats in per_stack {
        for c in cats {
            let a = agg.entry(&c.name).or_insert_with(|| Category {
                name: c.name.clone(),
                icon: c.icon.clone(),
                attributes: None,
            });
            a.attributes = merge_attrs(a.attributes.take(), c.attributes.as_ref());
        }
    }
    rules
        .iter()
        .filter_map(|r| agg.get(r.name.as_str()).cloned())
        .collect()
}

type Attrs = BTreeMap<String, Vec<String>>;

fn merge_attrs(a: Option<Attrs>, b: Option<&Attrs>) -> Option<Attrs> {
    let mut out: BTreeMap<String, BTreeSet<String>> = BTreeMap::new();
    for m in [a.as_ref(), b].into_iter().flatten() {
        for (k, vs) in m {
            out.entry(k.clone()).or_default().extend(vs.iter().cloned());
        }
    }
    if out.is_empty() {
        return None;
    }
    Some(out.into_iter().map(|(k, v)| (k, v.into_iter().collect())).collect())
}

fn extract(matched: &[&RawChange], names: &[String], derivations: &[Derivation]) -> Option<Attrs> {
    let mut out = Attrs::new();
    for name in names {
        let vals: BTreeSet<String> = matched
            .iter()
            .filter_map(|c| change_attr(c, name, derivations))
            .collect();
        if !vals.is_empty() {
            out.insert(name.clone(), vals.into_iter().collect());
        }
    }
    (!out.is_empty()).then_some(out)
}

fn change_attr(c: &RawChange, name: &str, derivations: &[Derivation]) -> Option<String> {
    if let Some(v) = c.raw.get(name)
        && !matches!(v, Value::Null)
    {
        return Some(scalar_string(v));
    }
    for d in derivations {
        if d.attribute != name {
            continue;
        }
        if d.type_pattern.as_ref().is_some_and(|tp| !tp.is_match(&c.type_)) {
            continue;
        }
        let Some(src) = c.raw.get(&d.from_attribute).and_then(Value::as_str) else {
            continue;
        };
        let Some(m) = d.pattern.find_submatch(src) else {
            continue;
        };
        if let Some(i) = d.pattern.subexp_index("value")
            && i > 0
            && !m[i].is_empty()
        {
            return Some(m[i].clone());
        }
        if m.len() > 1 && !m[1].is_empty() {
            return Some(m[1].clone());
        }
    }
    None
}

/// The single project the stack's changes share, or `None` for zero or several.
fn derive_stack_project(changes: &[RawChange]) -> Option<String> {
    let seen: BTreeSet<String> = changes
        .iter()
        .filter_map(|c| c.raw.get("project"))
        .filter(|v| !matches!(v, Value::Null))
        .map(scalar_string)
        .collect();
    if seen.len() == 1 {
        seen.into_iter().next()
    } else {
        None
    }
}

fn scalar_string(v: &Value) -> String {
    match v {
        Value::Str(s) => s.clone(),
        Value::Bool(b) => b.to_string(),
        Value::Float(f) => gofmt::format_f(*f),
        other => format!("{other:?}"),
    }
}

fn rule_matches(r: &Rule, c: &RawChange) -> bool {
    if r.type_pattern.as_ref().is_some_and(|tp| !tp.is_match(&c.type_)) {
        return false;
    }
    r.actions.iter().all(|want| c.actions.contains(want))
}

#[cfg(test)]
mod tests;
