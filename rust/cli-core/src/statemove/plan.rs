//! The subset of terraform's JSON plan and state formats (`terraform-json`)
//! the state faces read, with the same format-version checks `tfjson.Plan` and
//! `tfjson.State` make when unmarshalled.

use serde::{Deserialize, Deserializer};

use crate::gofmt::quote;

fn null_default<'de, D, T>(d: D) -> Result<T, D::Error>
where
    D: Deserializer<'de>,
    T: Default + Deserialize<'de>,
{
    Ok(Option::<T>::deserialize(d)?.unwrap_or_default())
}

/// A terraform plan (`terraform show -json <planfile>`).
#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
pub struct Plan {
    /// The plan format version.
    #[serde(default, deserialize_with = "null_default")]
    pub format_version: String,
    /// Planned resource changes.
    #[serde(default, deserialize_with = "null_default")]
    pub resource_changes: Vec<ResourceChange>,
    /// The state before the plan ran.
    #[serde(default)]
    pub prior_state: Option<State>,
}

/// One planned resource change.
#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
pub struct ResourceChange {
    /// The resource address.
    #[serde(default, deserialize_with = "null_default")]
    pub address: String,
    /// The address it had before a `moved` block.
    #[serde(default, deserialize_with = "null_default")]
    pub previous_address: String,
    /// `managed` or `data`.
    #[serde(default, deserialize_with = "null_default")]
    pub mode: String,
    /// The resource type.
    #[serde(default, rename = "type", deserialize_with = "null_default")]
    pub r#type: String,
    /// The provider's full name.
    #[serde(default, deserialize_with = "null_default")]
    pub provider_name: String,
    /// The change itself.
    #[serde(default)]
    pub change: Option<Change>,
}

/// A change's actions and before value.
#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
pub struct Change {
    /// `["create"]`, `["delete"]`, `["delete","create"]`, …
    #[serde(default, deserialize_with = "null_default")]
    pub actions: Vec<String>,
    /// The value before the change.
    #[serde(default)]
    pub before: serde_json::Value,
    /// Set when the change imports the resource.
    #[serde(default)]
    pub importing: Option<serde_json::Value>,
}

impl Change {
    fn only(&self, action: &str) -> bool {
        self.actions.len() == 1 && self.actions[0] == action
    }

    /// Exactly `["create"]`.
    pub fn is_create(&self) -> bool {
        self.only("create")
    }

    /// Exactly `["delete"]`.
    pub fn is_delete(&self) -> bool {
        self.only("delete")
    }

    /// Exactly `["read"]`.
    pub fn is_read(&self) -> bool {
        self.only("read")
    }

    /// Exactly `["no-op"]`.
    pub fn is_noop(&self) -> bool {
        self.only("no-op")
    }
}

/// A terraform state (`terraform show -json <statefile>`, or a plan's `prior_state`).
#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
pub struct State {
    /// The state format version.
    #[serde(default, deserialize_with = "null_default")]
    pub format_version: String,
    /// The resources.
    #[serde(default)]
    pub values: Option<StateValues>,
}

/// A state's values.
#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
pub struct StateValues {
    /// The root module.
    #[serde(default)]
    pub root_module: Option<StateModule>,
}

/// One module in a state.
#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
pub struct StateModule {
    /// The module's resources.
    #[serde(default, deserialize_with = "null_default")]
    pub resources: Vec<StateResource>,
    /// Its child modules.
    #[serde(default, deserialize_with = "null_default")]
    pub child_modules: Vec<StateModule>,
}

/// One resource in a state.
#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
pub struct StateResource {
    /// The resource address.
    #[serde(default, deserialize_with = "null_default")]
    pub address: String,
    /// `managed` or `data`.
    #[serde(default, deserialize_with = "null_default")]
    pub mode: String,
    /// The provider's full name.
    #[serde(default, deserialize_with = "null_default")]
    pub provider_name: String,
}

impl StateResource {
    /// A managed resource at `address` (test helper and fake-runner building block).
    pub fn managed(address: &str, provider: &str) -> Self {
        Self {
            address: address.to_string(),
            mode: "managed".to_string(),
            provider_name: provider.to_string(),
        }
    }
}

/// Checks a format version against `>= 0.1, < 2.0` (what both tfjson types accept).
fn check_version(kind: &str, v: &str) -> Result<(), String> {
    if v.is_empty() {
        return Err(format!(
            "unexpected {kind} input, format version is missing"
        ));
    }
    let parts: Option<Vec<u64>> = v.split('.').map(|p| p.parse().ok()).collect();
    let Some(mut parts) = parts.filter(|p| !p.is_empty() && p.len() <= 3) else {
        return Err(format!(
            "invalid format version {}: Malformed version: {v}",
            quote(v)
        ));
    };
    parts.resize(3, 0);
    let canon = format!("{}.{}.{}", parts[0], parts[1], parts[2]);
    let ok = (parts[0], parts[1]) >= (0, 1) && parts[0] < 2;
    if !ok {
        return Err(format!(
            "unsupported {kind} format version: {} does not satisfy \">= 0.1, < 2.0\"",
            quote(&canon)
        ));
    }
    Ok(())
}

/// Parses plan JSON as `json.Unmarshal(data, &tfjson.Plan{})` does: the plan and
/// any `prior_state` must carry a supported `format_version`.
pub fn parse_plan(data: &[u8]) -> Result<Plan, String> {
    let p: Plan = serde_json::from_slice(data).map_err(|e| e.to_string())?;
    if let Some(s) = &p.prior_state {
        check_version("state", &s.format_version)?;
    }
    check_version("plan", &p.format_version)?;
    Ok(p)
}

/// Parses state JSON as `tfjson.State` does, including its version check.
pub fn parse_state(data: &[u8]) -> Result<State, String> {
    let s: State = serde_json::from_slice(data).map_err(|e| e.to_string())?;
    check_version("state", &s.format_version)?;
    Ok(s)
}

/// The changed addresses (and previous addresses) of a destination plan, each
/// with its provider, as `state check` builds them from the renderer's
/// `plan.Parse`: data sources, reads and pure no-ops (no move, no import) are left out.
pub fn change_addresses(p: &Plan) -> super::AddressSet {
    let mut out = super::AddressSet::new();
    for rc in &p.resource_changes {
        let Some(ch) = &rc.change else { continue };
        if rc.mode == "data" || ch.is_read() {
            continue;
        }
        let moved = !rc.previous_address.is_empty() && rc.previous_address != rc.address;
        let imported = ch.importing.as_ref().is_some_and(|v| !v.is_null());
        if ch.is_noop() && !moved && !imported {
            continue;
        }
        out.insert(rc.address.clone(), rc.provider_name.clone());
        if !rc.previous_address.is_empty() {
            out.insert(rc.previous_address.clone(), rc.provider_name.clone());
        }
    }
    out
}

#[cfg(test)]
mod tests;
