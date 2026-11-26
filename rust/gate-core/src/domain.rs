//! Value types shared by the render pipeline, the runner wire protocol and
//! persistence (Go: `internal/domain`).

use std::collections::BTreeMap;

use serde::{Deserialize, Serialize};

use crate::json::{is_zero, nullable};

/// A stack's per-kind operation tally (pure no-ops excluded). Every field is left
/// out of the JSON when zero, as Go's `omitempty`.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Counts {
    #[serde(skip_serializing_if = "is_zero")]
    pub add: i64,
    #[serde(skip_serializing_if = "is_zero")]
    pub change: i64,
    #[serde(skip_serializing_if = "is_zero")]
    pub destroy: i64,
    #[serde(skip_serializing_if = "is_zero")]
    pub replace: i64,
    #[serde(rename = "move", skip_serializing_if = "is_zero")]
    pub moved: i64,
    #[serde(skip_serializing_if = "is_zero")]
    pub import: i64,
    #[serde(skip_serializing_if = "is_zero")]
    pub forget: i64,
    #[serde(skip_serializing_if = "is_zero")]
    pub sensitivity_only: i64,
}

impl Counts {
    /// The sum of the create, change, destroy and replace buckets.
    pub fn total(&self) -> i64 {
        self.add + self.change + self.destroy + self.replace
    }

    /// Whether the stack has anything worth rendering: a config change, or a move,
    /// import or forget.
    pub fn any_change(&self) -> bool {
        self.total() + self.moved + self.import + self.forget > 0
    }
}

/// One matched classification rule: its name, optional icon, and the sorted,
/// unique values of the attributes it emitted.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct Category {
    pub name: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub icon: String,
    #[serde(
        default,
        deserialize_with = "nullable",
        skip_serializing_if = "BTreeMap::is_empty"
    )]
    pub attributes: BTreeMap<String, Vec<String>>,
}

impl Category {
    /// "icon name", or just "name" without an icon.
    pub fn label(&self) -> String {
        if self.icon.is_empty() {
            self.name.clone()
        } else {
            format!("{} {}", self.icon, self.name)
        }
    }
}

#[cfg(test)]
mod tests;
