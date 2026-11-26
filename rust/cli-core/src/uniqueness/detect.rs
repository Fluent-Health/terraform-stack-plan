//! The two detectors: duplicates across environments, and foreign env tokens.

use std::collections::BTreeMap;

use regex::Regex;

use super::classify::boundary_token_pattern;
use super::{Classifier, Kind, Severity, Tier, Unit, Value, Violation};

/// Identifier-shaped values shared by two or more of a unit's environments.
/// A group spanning the protected tier and another tier is a violation; a group
/// within one tier is report-only. An env with no tier counts as protected.
pub fn find_duplicates(
    u: &Unit,
    tier_of: &BTreeMap<String, Tier>,
    protected: &str,
    c: &Classifier,
) -> Vec<Violation> {
    let mut keys: Vec<&String> = u.inputs.values().flat_map(|flat| flat.keys()).collect();
    keys.sort();
    keys.dedup();
    let mut out = Vec::new();
    for key in keys {
        if c.is_env_scoped(key) {
            continue;
        }
        out.extend(find_duplicates_for_key(u, key, tier_of, protected, c));
    }
    out
}

fn find_duplicates_for_key(
    u: &Unit,
    key: &str,
    tier_of: &BTreeMap<String, Tier>,
    protected: &str,
    c: &Classifier,
) -> Vec<Violation> {
    let mut groups: BTreeMap<String, (Value, Vec<String>)> = BTreeMap::new();
    for (env, flat) in &u.inputs {
        let Some(val) = flat.get(key) else { continue };
        groups
            .entry(group_key(val))
            .or_insert_with(|| (val.clone(), Vec::new()))
            .1
            .push(env.clone());
    }
    let mut out = Vec::new();
    for (value, mut envs) in groups.into_values() {
        if envs.len() < 2 || !c.is_identifier(key, &value) {
            continue;
        }
        envs.sort();
        let (mut has_protected, mut has_other) = (false, false);
        for e in &envs {
            let t = tier_of.get(e).map(String::as_str).unwrap_or(protected);
            if t == protected {
                has_protected = true;
            } else {
                has_other = true;
            }
        }
        let severity = if has_protected && has_other {
            Severity::Violation
        } else {
            Severity::ReportOnly
        };
        out.push(Violation {
            unit: u.id.clone(),
            key: key.to_string(),
            value,
            envs,
            kind: Kind::Duplicate,
            severity,
        });
    }
    out
}

/// Leaves in one environment that embed another environment's token as a
/// whole token: at most one violation per leaf.
pub fn find_env_tokens(u: &Unit, c: &Classifier) -> Vec<Violation> {
    let mut pats: BTreeMap<&str, Regex> = BTreeMap::new();
    for tok in c.env_tokens.values().flatten() {
        if !tok.is_empty() && !pats.contains_key(tok.as_str()) {
            pats.insert(tok, boundary_token_pattern(tok));
        }
    }
    let mut out = Vec::new();
    for (env, flat) in &u.inputs {
        let foreign: Vec<&String> = c
            .env_tokens
            .iter()
            .filter(|(f, _)| *f != env)
            .flat_map(|(_, toks)| toks)
            .collect();
        for (key, val) in flat {
            if c.is_env_scoped(key) {
                continue;
            }
            let candidates = env_token_candidates(val);
            if candidates.is_empty() {
                continue;
            }
            let leaks = foreign.iter().any(|tok| {
                pats.get(tok.as_str())
                    .is_some_and(|p| candidates.iter().any(|cand| p.is_match(cand)))
            });
            if leaks {
                out.push(Violation {
                    unit: u.id.clone(),
                    key: key.clone(),
                    value: val.clone(),
                    envs: vec![env.clone()],
                    kind: Kind::EnvToken,
                    severity: Severity::Violation,
                });
            }
        }
    }
    out
}

fn env_token_candidates(value: &Value) -> Vec<&str> {
    match value {
        Value::Str(s) if !s.is_empty() => vec![s.as_str()],
        Value::List(elems) => elems
            .iter()
            .filter(|e| !e.is_empty())
            .map(String::as_str)
            .collect(),
        _ => Vec::new(),
    }
}

/// A grouping key no list can share with a scalar.
pub fn group_key(value: &Value) -> String {
    match value {
        Value::List(elems) => format!("list\x1f{}", elems.join("\x1f")),
        other => format!("scalar\x1f{}", other.sprint()),
    }
}

#[cfg(test)]
mod tests;
