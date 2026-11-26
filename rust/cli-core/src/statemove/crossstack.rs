//! Moves between two stacks' plans: the native `import`/`removed` shims and the
//! generation-time check for `--via mv`.

use std::collections::{BTreeMap, BTreeSet};

use super::plan::{Plan, ResourceChange};
use super::shim::Op;
use super::{Move, matches, state_addresses};
use crate::gofmt::quote;

/// The destroyed resource's prior `id` (its import id).
fn import_id(rc: &ResourceChange) -> Result<String, String> {
    let id = rc
        .change
        .as_ref()
        .and_then(|c| c.before.get("id"))
        .and_then(|v| v.as_str())
        .unwrap_or_default();
    if id.is_empty() {
        return Err(format!(
            "{} has no string before.id to import with",
            quote(&rc.address)
        ));
    }
    Ok(id.to_string())
}

/// Go's `CrossStackPairs`: validates a cross-stack move and returns the matched
/// per-resource pairs, sorted by source address. Fail-closed like `ValidateMove`.
pub fn cross_stack_pairs(
    src: &Plan,
    dst: &Plan,
    from: &str,
    to: &str,
) -> Result<Vec<Move>, String> {
    if from.is_empty() || to.is_empty() {
        return Err(format!(
            "cross-stack move: empty address (from={} to={})",
            quote(from),
            quote(to)
        ));
    }
    let deletes: BTreeMap<&str, &ResourceChange> = src
        .resource_changes
        .iter()
        .filter(|rc| {
            rc.change.as_ref().is_some_and(|c| c.is_delete()) && matches(&rc.address, from)
        })
        .map(|rc| (rc.address.as_str(), rc))
        .collect();
    let creates: BTreeMap<&str, &ResourceChange> = dst
        .resource_changes
        .iter()
        .filter(|rc| rc.change.as_ref().is_some_and(|c| c.is_create()) && matches(&rc.address, to))
        .map(|rc| (rc.address.as_str(), rc))
        .collect();
    if deletes.is_empty() {
        return Err(format!(
            "cross-stack move {from} → {to}: nothing under {} is destroyed in the source plan",
            quote(from)
        ));
    }
    let mut pairs = Vec::new();
    let mut matched = BTreeSet::new();
    for (addr, d) in &deletes {
        let want = format!("{to}{}", &addr[from.len()..]);
        let Some(c) = creates.get(want.as_str()) else {
            return Err(format!(
                "cross-stack move {from} → {to}: {} is destroyed but {} is not created at the destination",
                quote(addr),
                quote(&want)
            ));
        };
        if c.r#type != d.r#type {
            return Err(format!(
                "cross-stack move {from} → {to}: type mismatch {} ({}) vs {} ({})",
                quote(addr),
                d.r#type,
                quote(&c.address),
                c.r#type
            ));
        }
        pairs.push(Move::new(addr, &want));
        matched.insert(want);
    }
    for caddr in creates.keys() {
        if !matched.contains(*caddr) {
            return Err(format!(
                "cross-stack move {from} → {to}: {} is created at the destination but has no destroyed counterpart under {}",
                quote(caddr),
                quote(from)
            ));
        }
    }
    Ok(pairs)
}

/// Go's `CheckXMoveSource`: the source plan's `prior_state` must hold something
/// under `from`.
pub fn check_xmove_source(src: &Plan, from: &str) -> Result<(), String> {
    let Some(state) = src.prior_state.as_ref().filter(|s| s.values.is_some()) else {
        return Err("source stack has no prior state — nothing to move".to_string());
    };
    if state_addresses(state).keys().any(|a| matches(a, from)) {
        return Ok(());
    }
    Err(format!(
        "nothing under {} in source prior state (wrong from address or stack?)",
        quote(from)
    ))
}

/// Go's `ClassifyCrossStack`: the ops a native cross-stack move writes —
/// `removed` in the source shim, `import` (with the source's `before.id`) in the
/// destination shim.
pub fn classify_cross_stack(
    src: &Plan,
    dst: &Plan,
    from: &str,
    to: &str,
) -> Result<(Vec<Op>, Vec<Op>), String> {
    let pairs = cross_stack_pairs(src, dst, from, to)?;
    let del_by_addr: BTreeMap<&str, &ResourceChange> = src
        .resource_changes
        .iter()
        .filter(|rc| rc.change.as_ref().is_some_and(|c| c.is_delete()))
        .map(|rc| (rc.address.as_str(), rc))
        .collect();
    let mut src_ops = Vec::new();
    let mut dst_ops = Vec::new();
    for p in &pairs {
        let rc = del_by_addr.get(p.from.as_str()).ok_or_else(|| {
            format!(
                "cross-stack move {from} → {to}: {} vanished",
                quote(&p.from)
            )
        })?;
        let id = import_id(rc).map_err(|e| format!("cross-stack move {from} → {to}: {e}"))?;
        src_ops.push(Op::removed(&p.from));
        dst_ops.push(Op::import(&p.to, &id));
    }
    Ok((src_ops, dst_ops))
}

#[cfg(test)]
mod tests;
