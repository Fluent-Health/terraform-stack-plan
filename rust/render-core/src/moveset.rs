//! The optional `--state-moves` manifest: per stack, the addresses an external cross-state
//! move brings in (`internal/moveset`).

use std::collections::{HashMap, HashSet};

use crate::gofmt::quote;
use crate::gojson;

/// One stack's pending move targets.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Set(HashSet<String>);

impl Set {
    /// A set of these addresses.
    pub fn new<I: IntoIterator<Item = S>, S: Into<String>>(addrs: I) -> Self {
        Set(addrs.into_iter().map(Into::into).collect())
    }

    /// No targets.
    pub fn is_empty(&self) -> bool {
        self.0.is_empty()
    }

    /// Whether `addr` is a target, or sits under one (`target.` for a moved module's children,
    /// `target[` for instances of a moved resource).
    pub fn covers(&self, addr: &str) -> bool {
        if self.0.contains(addr) {
            return true;
        }
        self.0.iter().any(|t| {
            addr.strip_prefix(t.as_str())
                .is_some_and(|rest| rest.starts_with('.') || rest.starts_with('['))
        })
    }
}

/// Move targets for every stack.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Manifest(HashMap<String, Set>);

impl Manifest {
    /// The targets for a stack (empty if none).
    pub fn targets(&self, stack: &str) -> Set {
        self.0.get(stack).cloned().unwrap_or_default()
    }

    /// Parses the manifest JSON (`{"<stack>": ["<addr>", ...]}`) read from `path`. Errors read
    /// like Go's.
    pub fn parse(path: &str, data: &[u8]) -> Result<Manifest, String> {
        let fail = |e: String| format!("parse state-moves {}: {e}", quote(path));
        gojson::check_valid(data).map_err(fail)?;
        let doc: serde_json::Value =
            serde_json::from_str(&String::from_utf8_lossy(data)).map_err(|e| fail(e.to_string()))?;
        let type_err = |v: &serde_json::Value, t: &str| {
            fail(format!(
                "json: cannot unmarshal {} into Go value of type {t}",
                gojson::type_name(v)
            ))
        };
        let mut out = HashMap::new();
        match &doc {
            serde_json::Value::Null => {}
            serde_json::Value::Object(o) => {
                for (stack, addrs) in o {
                    let set = match addrs {
                        serde_json::Value::Null => Set::default(),
                        serde_json::Value::Array(a) => {
                            let mut s = HashSet::new();
                            for x in a {
                                match x {
                                    serde_json::Value::String(v) => {
                                        s.insert(v.clone());
                                    }
                                    serde_json::Value::Null => {
                                        s.insert(String::new());
                                    }
                                    other => return Err(type_err(other, "string")),
                                }
                            }
                            Set(s)
                        }
                        other => return Err(type_err(other, "[]string")),
                    };
                    out.insert(stack.clone(), set);
                }
            }
            other => return Err(type_err(other, "map[string][]string")),
        }
        Ok(Manifest(out))
    }
}

#[cfg(test)]
mod tests;
