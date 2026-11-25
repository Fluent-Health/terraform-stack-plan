//! Which leaves look like identifiers, and which keys are env-scoped fan-out.

use std::collections::{BTreeMap, BTreeSet};

use regex::Regex;

use super::Value;
use crate::goregex;

fn compile_all(patterns: &[&str]) -> Vec<Regex> {
    patterns
        .iter()
        .filter_map(|p| goregex::compile(p).ok())
        .collect()
}

/// Value shapes that are identifiers whatever the key: a UUID, an http(s) URL, a
/// dotted hostname with a TLD, a long opaque number.
pub fn default_value_patterns() -> Vec<Regex> {
    compile_all(&[
        r"^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$",
        r"^https?://",
        r"(?:[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,}",
        r"^\d{6,}$",
    ])
}

/// Key names that are identifiers by name.
pub fn default_key_patterns() -> Vec<Regex> {
    compile_all(&[
        "_id$",
        "_uuid$",
        "client_id$",
        "project_id$",
        "domain_id$",
        "phone_number_id$",
        "account_id$",
        "app_id$",
        "system_user_id$",
        "org_id$",
    ])
}

/// Key suffixes for quantities, durations and counts: never identifiers.
pub fn default_quantity_suffixes() -> Vec<String> {
    [
        "_ms",
        "_seconds",
        "_sec",
        "_days",
        "_count",
        "_size",
        "_size_gb",
        "_bytes",
        "_port",
        "_replicas",
        "_percent",
        "_validity",
        "_validity_seconds",
        "_expiration",
        "_timeout",
        "_lead_days",
        "_max_age_days",
    ]
    .iter()
    .map(|s| s.to_string())
    .collect()
}

/// Matches `tok` as a whole token: not flanked by an ASCII letter or digit.
pub fn boundary_token_pattern(tok: &str) -> Regex {
    #[expect(
        clippy::expect_used,
        reason = "an escaped literal between fixed classes always compiles"
    )]
    Regex::new(&format!(
        "(?:^|[^0-9A-Za-z]){}(?:$|[^0-9A-Za-z])",
        regex::escape(tok)
    ))
    .expect("escaped token pattern")
}

fn compile_token_patterns(env_tokens: &BTreeMap<String, Vec<String>>) -> Vec<Regex> {
    env_tokens
        .values()
        .flatten()
        .filter(|t| !t.is_empty())
        .map(|t| boundary_token_pattern(t))
        .collect()
}

/// Decides identifier shape and env scoping (Go's `Classifier`). Build it with
/// [`Classifier::new`] to precompile the token patterns; a struct built field by
/// field (`token_pats: None`) compiles them on each call.
#[derive(Clone, Debug, Default)]
pub struct Classifier {
    /// Value-shape patterns.
    pub val_pats: Vec<Regex>,
    /// Key-name patterns (matched against the last dot segment).
    pub key_pats: Vec<Regex>,
    /// Quantity suffixes.
    pub qty_suffixes: Vec<String>,
    /// Identifying tokens per environment.
    pub env_tokens: BTreeMap<String, Vec<String>>,
    /// Lowercased env-scoped key segments.
    pub scoped_segs: BTreeSet<String>,
    /// Precompiled boundary patterns for `env_tokens`.
    pub token_pats: Option<Vec<Regex>>,
}

impl Classifier {
    /// `NewClassifier`.
    pub fn new(
        val_pats: Vec<Regex>,
        key_pats: Vec<Regex>,
        qty_suffixes: Vec<String>,
        env_tokens: BTreeMap<String, Vec<String>>,
        scoped_segs: BTreeSet<String>,
    ) -> Self {
        let token_pats = Some(compile_token_patterns(&env_tokens));
        Self {
            val_pats,
            key_pats,
            qty_suffixes,
            env_tokens,
            scoped_segs,
            token_pats,
        }
    }

    /// Whether a leaf is identifier-shaped. Bools, nil and empty strings never
    /// are; a list is if any element is.
    pub fn is_identifier(&self, key: &str, value: &Value) -> bool {
        match value {
            Value::Bool(_) | Value::Nil => return false,
            Value::List(elems) => {
                return elems
                    .iter()
                    .any(|e| self.is_identifier(key, &Value::Str(e.clone())));
            }
            _ => {}
        }
        let s = stringify_value(value);
        if s.is_empty() {
            return false;
        }
        let leaf = key.rsplit('.').next().unwrap_or(key);
        if self
            .qty_suffixes
            .iter()
            .any(|suf| leaf.ends_with(suf.as_str()))
        {
            return false;
        }
        if self.key_pats.iter().any(|p| p.is_match(leaf)) {
            return true;
        }
        if self.val_pats.iter().any(|p| p.is_match(&s)) {
            return true;
        }
        self.matches_env_token(&s)
    }

    /// Whether any lowercased dot segment of the key is env-scoped.
    pub fn is_env_scoped(&self, key: &str) -> bool {
        key.split('.')
            .any(|seg| self.scoped_segs.contains(&seg.to_lowercase()))
    }

    fn matches_env_token(&self, s: &str) -> bool {
        match &self.token_pats {
            Some(p) => p.iter().any(|p| p.is_match(s)),
            None => compile_token_patterns(&self.env_tokens)
                .iter()
                .any(|p| p.is_match(s)),
        }
    }
}

/// `stringifyValue`: `fmt.Sprint`, except a finite whole float prints without
/// an exponent so a huge YAML integer still looks like a long number.
pub fn stringify_value(value: &Value) -> String {
    if let Value::Float(f) = value
        && f.is_finite()
        && *f == f.trunc()
    {
        return crate::gofmt::format_f(*f);
    }
    value.sprint()
}

/// Per environment, the tokens that identify it (`template` with `{env}`
/// replaced, the env name, then `extra_tokens`), and every env-scoped segment.
pub fn derive_tokens_and_segs(
    envs: &[String],
    template: &str,
    extra_tokens: &[(String, Vec<String>)],
    extra_segs: &[String],
) -> (BTreeMap<String, Vec<String>>, BTreeSet<String>) {
    let mut tokens: BTreeMap<String, Vec<String>> = BTreeMap::new();
    let mut segs = BTreeSet::new();
    for env in envs {
        let mut toks = Vec::new();
        if !template.is_empty() {
            toks.push(template.replace("{env}", env));
        }
        toks.push(env.clone());
        tokens.insert(env.clone(), toks);
        segs.insert(env.to_lowercase());
    }
    for (env, extras) in extra_tokens {
        tokens
            .entry(env.clone())
            .or_default()
            .extend(extras.iter().cloned());
    }
    for tok in tokens.values().flatten() {
        segs.insert(tok.to_lowercase());
    }
    for seg in extra_segs {
        segs.insert(seg.to_lowercase());
    }
    (tokens, segs)
}

#[cfg(test)]
mod tests;
