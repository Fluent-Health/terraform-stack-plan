use std::collections::BTreeSet;

use jiff::Timestamp;

use super::ClaimSet;

/// The answer of a [`held`] query.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Verdict {
    pub held: bool,
    /// The blocked stacks, sorted.
    pub blocking: Vec<String>,
    /// The PRs owning the blocking claims, sorted and unique.
    pub blocking_prs: Vec<i64>,
}

/// Whether any of `pr`'s stacks is claimed by another PR with a lease unexpired at
/// `now`.
pub fn held(s: &ClaimSet, pr: i64, stacks: &[String], now: Timestamp) -> Verdict {
    let mut blocking: Vec<String> = vec![];
    let mut prs = BTreeSet::new();
    for stack in stacks {
        if let Some(c) = s.get(stack).filter(|c| c.pr != pr && c.expires_at > now) {
            blocking.push(stack.clone());
            prs.insert(c.pr);
        }
    }
    blocking.sort();
    Verdict {
        held: !blocking.is_empty(),
        blocking,
        blocking_prs: prs.into_iter().collect(),
    }
}

#[cfg(test)]
mod tests;
