//! The cross-state executor's decisions against live state, and the plan-time
//! validation `state check` runs.

use std::collections::BTreeSet;
use std::sync::LazyLock;

use regex::Regex;

use super::{AddressSet, Move, XMove, matches};
use crate::gofmt::quote;

/// Provider short names configured in a destination stack.
pub type DestProviders = BTreeSet<String>;

/// How bad a diagnostic is.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Severity {
    /// Fails the check.
    Error,
    /// Reported, does not fail.
    Warning,
}

/// One validation finding.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Diagnostic {
    /// `xmove/<code>`.
    pub code: String,
    /// Error or warning.
    pub severity: Severity,
    /// What is wrong.
    pub message: String,
}

fn diag(code: &str, severity: Severity, message: String) -> Diagnostic {
    Diagnostic {
        code: code.to_string(),
        severity,
        message,
    }
}

/// The per-pair runtime action.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Decision {
    /// Source has it, destination does not: move it.
    Move,
    /// Destination already has it: skip (idempotent re-run).
    Skip,
}

/// Go's `expandPairs`: resolves declared pairs against live source/destination
/// addresses. A module or prefix pair fans out to the concrete resources under it
/// (longest declared prefix wins); an already-moved pair is re-keyed from the
/// destination's children; a pair matching neither side is kept as declared so
/// [`decide`] fails closed.
pub fn expand_pairs(src: &AddressSet, dst: &AddressSet, pairs: &[Move]) -> Vec<Move> {
    let mut out = Vec::new();
    for p in pairs {
        let src_hits: Vec<&String> = src
            .keys()
            .filter(|a| matches(a, &p.from))
            .filter(|a| {
                !pairs
                    .iter()
                    .any(|o| o.from != p.from && o.from.len() > p.from.len() && matches(a, &o.from))
            })
            .collect();
        let dst_hits: Vec<&String> = dst
            .keys()
            .filter(|a| matches(a, &p.to))
            .filter(|a| {
                !pairs
                    .iter()
                    .any(|o| o.to != p.to && o.to.len() > p.to.len() && matches(a, &o.to))
            })
            .collect();
        if !src_hits.is_empty() {
            for s in src_hits {
                out.push(Move::new(s, &format!("{}{}", p.to, &s[p.from.len()..])));
            }
        } else if !dst_hits.is_empty() {
            for d in dst_hits {
                out.push(Move::new(&format!("{}{}", p.from, &d[p.to.len()..]), d));
            }
        } else {
            out.push(p.clone());
        }
    }
    out
}

/// Go's `decide`: the fail-closed idempotency table for one exact pair.
pub fn decide(
    src: &AddressSet,
    dst: &AddressSet,
    from: &str,
    to: &str,
) -> Result<Decision, String> {
    match (src.contains_key(from), dst.contains_key(to)) {
        (true, false) => Ok(Decision::Move),
        (false, true) => Ok(Decision::Skip),
        (true, true) => Err(format!(
            "ambiguous: {} is in the source state AND {} is in the destination state (would duplicate)",
            quote(from),
            quote(to)
        )),
        (false, false) => Err(format!(
            "missing: {} is not in the source state and {} is not in the destination state (manifest wrong or already pruned)",
            quote(from),
            quote(to)
        )),
    }
}

/// Providers that need no configuration in the destination stack.
const IMPLICIT_PROVIDERS: &[&str] = &[
    "random",
    "null",
    "local",
    "tls",
    "archive",
    "template",
    "time",
    // terraform.io/builtin/terraform backs terraform_data and cannot be declared.
    "terraform",
];

/// Go's `modulePrefix`: the module path of an address (`module.a.module.b` for
/// `module.a.module.b.res.x`, empty for a root resource).
pub fn module_prefix(addr: &str) -> String {
    let parts: Vec<&str> = addr.split('.').collect();
    let mut end = parts.len();
    if end >= 2 && parts[end - 2] != "module" {
        end -= 2;
    }
    parts[..end].join(".")
}

/// Data sources under any pair's `from`: they stay in the source stack after the move.
pub fn data_source_orphans(pairs: &[Move], data_sources: &[String]) -> Vec<String> {
    data_sources
        .iter()
        .filter(|ds| pairs.iter().any(|p| matches(ds, &p.from)))
        .cloned()
        .collect()
}

fn applied_to(dst_prior: &AddressSet, to: &str) -> bool {
    dst_prior.keys().any(|a| matches(a, to))
}

/// Whether every pair's `to` already has something in the destination's prior
/// state (false when that state or the pairs are empty).
pub fn is_spent(pairs: &[Move], dst_prior: &AddressSet) -> bool {
    if dst_prior.is_empty() || pairs.is_empty() {
        return false;
    }
    pairs.iter().all(|p| applied_to(dst_prior, &p.to))
}

fn short_name(full: &str) -> &str {
    full.rsplit('/').next().unwrap_or(full)
}

fn provider_missing(prov: &str, providers: &DestProviders, to: &str) -> Option<Diagnostic> {
    if prov.is_empty() {
        return None;
    }
    let short = short_name(prov);
    if IMPLICIT_PROVIDERS.contains(&short) || short == "module" || providers.contains(short) {
        return None;
    }
    Some(diag(
        "xmove/provider-missing",
        Severity::Error,
        format!(
            "destination stack has no {} provider configured, but will receive resource {} requiring it",
            quote(short),
            quote(to)
        ),
    ))
}

/// Go's `ValidateMovePlan`. At apply time (`is_apply`) `src`/`dst` are the live
/// states; at plan time `src` is the source's prior state, `dst` the destination
/// plan's changed addresses and `dst_prior` the destination's prior state (used
/// to tell a spent entry from a stale one).
///
/// Go picks the destination provider for a module-level pair by iterating a map,
/// so which candidate it compares is random; this walks the addresses in sorted
/// order and takes the first candidate.
pub fn validate_move_plan(
    src: &AddressSet,
    dst: &AddressSet,
    dst_prior: &AddressSet,
    providers: &DestProviders,
    m: &XMove,
    is_apply: bool,
) -> Vec<Diagnostic> {
    let mut diags = Vec::new();
    for p in expand_pairs(src, dst, &m.pairs) {
        let src_prov = src.get(&p.from);
        let dst_prov = dst.get(&p.to);
        let prov = src_prov.map(String::as_str).unwrap_or_default();
        if is_apply {
            match (src_prov.is_some(), dst_prov.is_some()) {
                (true, false) => diags.extend(provider_missing(prov, providers, &p.to)),
                (false, true) => {}
                (true, true) => diags.push(diag(
                    "xmove/dest-occupied",
                    Severity::Error,
                    format!(
                        "ambiguous: {} is in the source state AND {} is in the destination state (would duplicate)",
                        quote(&p.from),
                        quote(&p.to)
                    ),
                )),
                (false, false) => diags.push(diag(
                    "xmove/source-missing",
                    Severity::Error,
                    format!(
                        "missing: {} is not in the source state and {} is not in the destination state (manifest wrong or already pruned)",
                        quote(&p.from),
                        quote(&p.to)
                    ),
                )),
            }
            continue;
        }
        let in_src = src_prov.is_some();
        let in_dst = dst_prov.is_some();
        if !in_src {
            if applied_to(dst_prior, &p.to) {
                diags.push(diag(
                    "xmove/spent",
                    Severity::Warning,
                    format!(
                        "{} already moved to {} — entry already applied; run 'state cleanup --applied' to remove this manifest",
                        quote(&p.from),
                        quote(&p.to)
                    ),
                ));
                continue;
            }
            diags.push(diag(
                "xmove/source-missing",
                Severity::Error,
                format!(
                    "address {} not found in source plan (manifest stale or address renamed)",
                    quote(&p.from)
                ),
            ));
        } else {
            diags.extend(provider_missing(prov, providers, &p.to));
        }
        if !dst.is_empty() && !in_dst {
            diags.push(diag(
                "xmove/dest-missing",
                Severity::Error,
                format!(
                    "address {} not found in destination plan changes (manifest stale or target address incorrect)",
                    quote(&p.to)
                ),
            ));
        }
        let mut effective = dst_prov.cloned().unwrap_or_default();
        if !in_dst && !dst.is_empty() {
            let to_mod = module_prefix(&p.to);
            for (addr, ap) in dst {
                let candidate = if to_mod.is_empty() {
                    !addr.contains(".module.") && !addr.starts_with("module.")
                } else {
                    matches(addr, &to_mod)
                };
                if candidate {
                    effective = ap.clone();
                    break;
                }
            }
        }
        if in_src
            && !dst.is_empty()
            && !prov.is_empty()
            && !effective.is_empty()
            && prov != effective
        {
            diags.push(diag(
                "xmove/provider-mismatch",
                Severity::Error,
                format!(
                    "provider mismatch: {} uses {} but destination {} uses {}",
                    quote(&p.from),
                    short_name(prov),
                    quote(&p.to),
                    short_name(&effective)
                ),
            ));
        }
    }
    diags
}

static PROVIDER_RE: LazyLock<Regex> = LazyLock::new(|| {
    #[expect(
        clippy::expect_used,
        reason = "a constant pattern; covered by the tests"
    )]
    crate::goregex::compile(r#"provider\s+"([^"]+)"\s*\{"#).expect("valid provider pattern")
});

/// Go's `DiscoverDestProviders` over the contents of a stack's `*.tf` files:
/// every `provider "<name>" {` block's name.
pub fn dest_providers<'a>(tf_files: impl IntoIterator<Item = &'a str>) -> DestProviders {
    let mut out = DestProviders::new();
    for content in tf_files {
        for c in PROVIDER_RE.captures_iter(content) {
            out.insert(c[1].to_string());
        }
    }
    out
}

#[cfg(test)]
mod tests;
