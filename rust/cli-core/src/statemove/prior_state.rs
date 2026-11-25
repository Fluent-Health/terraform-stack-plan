//! Addresses in a state or a plan's `prior_state`.

use serde::Deserialize;

use super::AddressSet;
use super::plan::{State, StateModule, StateValues};

fn walk<'a>(m: &'a StateModule, f: &mut impl FnMut(&'a super::StateResource)) {
    for r in &m.resources {
        f(r);
    }
    for c in &m.child_modules {
        walk(c, f);
    }
}

/// Every managed resource address in a state (root and child modules) with its
/// provider. Data sources are left out, so a module-level move never sweeps a
/// `data.*` address into the move set.
pub fn state_addresses(s: &State) -> AddressSet {
    let mut out = AddressSet::new();
    if let Some(root) = s.values.as_ref().and_then(|v| v.root_module.as_ref()) {
        walk(root, &mut |r| {
            if r.mode != "data" {
                out.insert(r.address.clone(), r.provider_name.clone());
            }
        });
    }
    out
}

/// `prior_state.values` read leniently (no format-version check), as Go's
/// lightweight extraction struct does.
#[derive(Deserialize)]
struct RawPlan {
    #[serde(default)]
    prior_state: Option<RawPrior>,
}

#[derive(Deserialize)]
struct RawPrior {
    #[serde(default)]
    values: Option<StateValues>,
}

fn prior_values(plan_json: &[u8]) -> Option<State> {
    let raw: RawPlan = serde_json::from_slice(plan_json).ok()?;
    let prior = raw.prior_state?;
    Some(State {
        format_version: String::new(),
        values: prior.values,
    })
}

/// The managed addresses in a plan's `prior_state`: the addresses live state
/// holds before the plan's `moved` blocks rename anything. `None` when the JSON
/// does not parse, there is no `prior_state`, or it holds no managed resources.
pub fn prior_state_addrs(plan_json: &[u8]) -> Option<AddressSet> {
    let addrs = state_addresses(&prior_values(plan_json)?);
    (!addrs.is_empty()).then_some(addrs)
}

/// The data-source addresses in a plan's `prior_state` (`None` when there is no
/// `prior_state` or the JSON does not parse).
pub fn prior_state_data_sources(plan_json: &[u8]) -> Option<Vec<String>> {
    let s = prior_values(plan_json)?;
    let mut out = Vec::new();
    if let Some(root) = s.values.as_ref().and_then(|v| v.root_module.as_ref()) {
        walk(root, &mut |r| {
            if r.mode == "data" {
                out.push(r.address.clone());
            }
        });
    }
    Some(out)
}

#[cfg(test)]
mod tests;
