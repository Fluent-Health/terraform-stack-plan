//! The two-sided `--state-moves` manifest (`state moves-manifest`): per stack,
//! the addresses moving in (planned creates) and out (planned destroys).

use std::collections::{BTreeMap, BTreeSet};

use super::{Op, XMove};
use crate::gojson::Value;

/// A discovered shim: its stack (relative to the walk root) and ops.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct FoundShim {
    /// Directory holding it, relative to the root (slash form, `.` for the root).
    pub stack: String,
    /// Filename key.
    pub key: String,
    /// Its ops.
    pub ops: Vec<Op>,
}

/// A discovered xmove manifest.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct FoundXMove {
    /// The file's path as the walk produced it.
    pub path: String,
    /// Directory holding it (the destination stack), relative to the root.
    pub dest_stack: String,
    /// Filename key.
    pub key: String,
    /// Its move.
    pub xmove: XMove,
}

/// Go's `resolveSourceStack`: `source_stack` may be relative to the `--dir` the
/// manifest was generated with. Try it under each ancestor prefix of
/// `dest_stack`, shortest first, and take the first that `exists`; else keep it.
pub fn resolve_source_stack(
    dest_stack: &str,
    source: &str,
    exists: &dyn Fn(&str) -> bool,
) -> String {
    let parts: Vec<&str> = dest_stack.split('/').collect();
    for i in 0..parts.len() {
        let candidate = if i == 0 {
            source.to_string()
        } else {
            format!("{}/{source}", parts[..i].join("/"))
        };
        if exists(&candidate) {
            return candidate;
        }
    }
    source.to_string()
}

/// Go's `collectStateMoves`: every pending move, optionally only key `want`.
pub fn collect_state_moves(
    shims: &[FoundShim],
    xmoves: &[FoundXMove],
    want: &str,
    exists: &dyn Fn(&str) -> bool,
) -> BTreeMap<String, Vec<String>> {
    let mut by_stack: BTreeMap<String, BTreeSet<String>> = BTreeMap::new();
    let mut add = |stack: &str, addr: &str| {
        if stack.is_empty() || addr.is_empty() {
            return;
        }
        by_stack
            .entry(stack.to_string())
            .or_default()
            .insert(addr.to_string());
    };
    for s in shims.iter().filter(|s| want.is_empty() || s.key == want) {
        for o in &s.ops {
            match o.kind.as_str() {
                "removed" => add(&s.stack, &o.from),
                "import" | "moved" => add(&s.stack, &o.to),
                _ => {}
            }
        }
    }
    for fx in xmoves.iter().filter(|x| want.is_empty() || x.key == want) {
        let source = resolve_source_stack(&fx.dest_stack, &fx.xmove.source_stack, exists);
        for p in &fx.xmove.pairs {
            add(&source, &p.from);
            add(&fx.dest_stack, &p.to);
        }
    }
    by_stack
        .into_iter()
        .map(|(k, v)| (k, v.into_iter().collect()))
        .collect()
}

/// The manifest as Go's `json.MarshalIndent(map[string][]string, "", "  ")` plus a newline.
pub fn manifest_json(m: &BTreeMap<String, Vec<String>>) -> String {
    let v = Value::Object(
        m.iter()
            .map(|(k, addrs)| (k.clone(), Value::strings(addrs)))
            .collect(),
    );
    let mut s = crate::gojson::marshal_indent(&v, "  ").unwrap_or_default();
    s.push('\n');
    s
}

#[cfg(test)]
mod tests;
