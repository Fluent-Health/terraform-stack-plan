//! `Evaluate`: tiers, tokens, both detectors and the allow partition.

use std::collections::{BTreeMap, BTreeSet};

use super::allow::{Date, partition};
use super::classify::{
    Classifier, default_key_patterns, default_quantity_suffixes, default_value_patterns,
    derive_tokens_and_segs,
};
use super::detect::{find_duplicates, find_env_tokens};
use super::{AllowRule, Report, Severity, Tier, Unit, Violation};
use crate::config::{EnvBlock, EnvUniquenessConfig};
use crate::gofmt::quote;

/// Runs the lint over `units` as of `today`.
pub fn evaluate(cfg: &EnvUniquenessConfig, units: &[Unit], today: Date) -> Result<Report, String> {
    let env_names: Vec<String> = units
        .iter()
        .flat_map(|u| u.inputs.keys().cloned())
        .collect::<BTreeSet<_>>()
        .into_iter()
        .collect();

    let tier_of = if cfg.source.tier_input.is_empty() {
        tiers_from_declared(&cfg.environments, &env_names)?
    } else {
        tiers_from_data(&cfg.source.tier_input, units, &env_names)?
    };

    let (tokens, segs) = derive_tokens_and_segs(
        &env_names,
        &cfg.project_token_template,
        &cfg.extra_env_tokens,
        &cfg.extra_scoped_segments,
    );

    let mut key_pats = default_key_patterns();
    for p in &cfg.extra_key_patterns {
        let re = crate::goregex::compile(p).map_err(|e| {
            format!(
                "env_uniqueness: invalid extra_key_patterns entry {}: {e}",
                quote(p)
            )
        })?;
        key_pats.push(re);
    }
    let classifier = Classifier::new(
        default_value_patterns(),
        key_pats,
        default_quantity_suffixes(),
        tokens,
        segs,
    );

    let protected = if cfg.protected_tier.is_empty() {
        "prod"
    } else {
        cfg.protected_tier.as_str()
    };

    let stripped;
    let detect_units = if cfg.source.tier_input.is_empty() {
        units
    } else {
        stripped = strip_leaf(units, &cfg.source.tier_input);
        &stripped[..]
    };

    let mut all: Vec<Violation> = Vec::new();
    for u in detect_units {
        all.extend(find_duplicates(u, &tier_of, protected, &classifier));
        all.extend(find_env_tokens(u, &classifier));
    }

    let allows: Vec<AllowRule> = cfg
        .allows
        .iter()
        .map(|a| AllowRule {
            unit: a.unit.clone(),
            key: a.key.clone(),
            envs: a.envs.clone(),
            reason: a.reason.clone(),
            expires: a.expires.clone(),
        })
        .collect();
    let (unjustified, stale) = partition(&all, &allows, today);
    let report_only = all
        .into_iter()
        .filter(|v| v.severity == Severity::ReportOnly)
        .collect();
    Ok(Report {
        unjustified,
        stale,
        report_only,
    })
}

fn tiers_from_data(
    tier_input: &str,
    units: &[Unit],
    env_names: &[String],
) -> Result<BTreeMap<String, Tier>, String> {
    let mut tier_of = BTreeMap::new();
    for env in env_names {
        let mut resolved: Option<String> = None;
        for u in units {
            let Some(raw) = u.inputs.get(env).and_then(|flat| flat.get(tier_input)) else {
                continue;
            };
            let t = raw.sprint();
            match &resolved {
                None => resolved = Some(t),
                Some(r) if *r != t => {
                    return Err(format!(
                        "env_uniqueness: inconsistent tier for env {}: {} vs {} (source.tier_input={})",
                        quote(env),
                        quote(r),
                        quote(&t),
                        quote(tier_input)
                    ));
                }
                Some(_) => {}
            }
        }
        let Some(r) = resolved else {
            return Err(format!(
                "env_uniqueness: no resolvable tier for env {} (source.tier_input={})",
                quote(env),
                quote(tier_input)
            ));
        };
        tier_of.insert(env.clone(), r);
    }
    Ok(tier_of)
}

fn tiers_from_declared(
    declared: &[EnvBlock],
    env_names: &[String],
) -> Result<BTreeMap<String, Tier>, String> {
    let mut by_name = BTreeMap::new();
    for e in declared {
        by_name.insert(e.name.clone(), e.tier.clone());
    }
    let mut tier_of = BTreeMap::new();
    for env in env_names {
        let Some(t) = by_name.get(env) else {
            return Err(format!(
                "env_uniqueness: environment {} found in data but not declared in any environment {{}} block",
                quote(env)
            ));
        };
        tier_of.insert(env.clone(), t.clone());
    }
    Ok(tier_of)
}

/// Copies of `units` without the tier leaf, so it never reaches the detectors.
fn strip_leaf(units: &[Unit], leaf: &str) -> Vec<Unit> {
    units
        .iter()
        .map(|u| {
            let mut u = u.clone();
            for flat in u.inputs.values_mut() {
                flat.remove(leaf);
            }
            u
        })
        .collect()
}

#[cfg(test)]
mod tests;
