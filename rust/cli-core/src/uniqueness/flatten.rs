//! `Flatten`: nested maps to dot paths, lists to the `List` sentinel.

use std::collections::BTreeMap;

use super::Value;

/// Collapses a decoded `map[string]any` into one level keyed by dot paths. A
/// list becomes [`Value::List`] with each element `fmt.Sprint`ed; any other leaf
/// (including a `map[any]any`) passes through.
pub fn flatten(inputs: &[(String, Value)]) -> BTreeMap<String, Value> {
    let mut out = BTreeMap::new();
    for (k, v) in inputs {
        flatten_into(&mut out, v, k);
    }
    out
}

fn flatten_into(out: &mut BTreeMap<String, Value>, obj: &Value, prefix: &str) {
    match obj {
        Value::Map(entries) => {
            for (k, v) in entries {
                flatten_into(out, v, &join_path(prefix, k));
            }
        }
        Value::Seq(items) => {
            out.insert(
                prefix.to_string(),
                Value::List(items.iter().map(Value::sprint).collect()),
            );
        }
        other => {
            out.insert(prefix.to_string(), other.clone());
        }
    }
}

fn join_path(prefix: &str, key: &str) -> String {
    if prefix.is_empty() {
        key.to_string()
    } else {
        format!("{prefix}.{key}")
    }
}

#[cfg(test)]
mod tests;
