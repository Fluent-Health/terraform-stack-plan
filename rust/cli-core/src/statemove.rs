//! Declared Terraform state moves (Go's `internal/statemove`): validating moves
//! against plan JSON, the `_tfsp_move.<key>.tf` shims and `_tfsp_xmove.<key>.hcl`
//! manifests that carry them, and the decisions the cross-state executor makes
//! against live state. Pure: the binary walks the tree, runs terraform and hands
//! the file contents and state data in.

use std::collections::BTreeMap;

pub mod backend;
pub mod crossstack;
pub mod decide;
pub mod manifest;
pub mod paths;
pub mod plan;
pub mod prior_state;
pub mod shim;
pub mod xmove;

pub use crossstack::{check_xmove_source, classify_cross_stack, cross_stack_pairs};
pub use decide::{
    DestProviders, Diagnostic, Severity, data_source_orphans, decide, dest_providers, expand_pairs,
    is_spent, module_prefix, validate_move_plan,
};
pub use plan::{Plan, ResourceChange, State, StateModule, StateResource};
pub use prior_state::{prior_state_addrs, prior_state_data_sources, state_addresses};
pub use shim::{Op, merge_ops, parse_shim, render_shim, shim_file_name, shim_key_from_file};
pub use xmove::{XMove, parse_xmove, render_xmove, xmove_file_name, xmove_key_from_file};

/// Resource address → provider name.
pub type AddressSet = BTreeMap<String, String>;

/// One declared relocation, from one address to another.
#[derive(Clone, Debug, Default, PartialEq, Eq, PartialOrd, Ord, Hash)]
pub struct Move {
    /// The source address (resource, module, or with count/for_each brackets).
    pub from: String,
    /// The destination address.
    pub to: String,
}

impl Move {
    /// A move from `from` to `to`.
    pub fn new(from: &str, to: &str) -> Self {
        Self {
            from: from.to_string(),
            to: to.to_string(),
        }
    }
}

/// Whether `addr` is `target` itself or a child of it (a module's resources, or a
/// resource's instances). The `.`/`[` boundary keeps `module.x` from matching
/// `module.x_other`.
pub fn matches(addr: &str, target: &str) -> bool {
    addr == target
        || addr
            .strip_prefix(target)
            .is_some_and(|rest| rest.starts_with('.') || rest.starts_with('['))
}

/// Go's `ValidateMove`: fails closed unless the plan shows a clean relocation
/// `from` → `to` within one stack: every destroy under `from` has a create at the
/// rewritten address under `to` with the same type, and no create under `to` is
/// left unmatched.
pub fn validate_move(plan: &Plan, from: &str, to: &str) -> Result<(), String> {
    use crate::gofmt::quote;
    if from.is_empty() || to.is_empty() {
        return Err(format!(
            "move: empty address (from={} to={})",
            quote(from),
            quote(to)
        ));
    }
    if from == to {
        return Err(format!("move: from and to are identical ({})", quote(from)));
    }
    let mut deletes: BTreeMap<&str, &ResourceChange> = BTreeMap::new();
    let mut creates: BTreeMap<&str, &ResourceChange> = BTreeMap::new();
    for rc in &plan.resource_changes {
        let Some(ch) = &rc.change else { continue };
        if ch.is_delete() && matches(&rc.address, from) {
            deletes.insert(&rc.address, rc);
        } else if ch.is_create() && matches(&rc.address, to) {
            creates.insert(&rc.address, rc);
        }
    }
    if deletes.is_empty() {
        return Err(format!(
            "move {from} → {to}: nothing under {} is being destroyed in the plan",
            quote(from)
        ));
    }
    let mut matched = std::collections::BTreeSet::new();
    for (addr, d) in &deletes {
        let want = format!("{to}{}", &addr[from.len()..]);
        let Some(c) = creates.get(want.as_str()) else {
            return Err(format!(
                "move {from} → {to}: {} is destroyed but {} is not created (plan not in order)",
                quote(addr),
                quote(&want)
            ));
        };
        if c.r#type != d.r#type {
            return Err(format!(
                "move {from} → {to}: type mismatch {} ({}) vs {} ({})",
                quote(addr),
                d.r#type,
                quote(&c.address),
                c.r#type
            ));
        }
        matched.insert(want);
    }
    for caddr in creates.keys() {
        if !matched.contains(*caddr) {
            return Err(format!(
                "move {from} → {to}: {} is created but has no destroyed counterpart under {}",
                quote(caddr),
                quote(from)
            ));
        }
    }
    Ok(())
}

#[cfg(test)]
mod tests;
