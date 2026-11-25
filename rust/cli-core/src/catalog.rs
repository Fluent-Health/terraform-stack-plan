//! `catalog build`: group stacks into components and connect them by watch
//! paths and run-graph dependencies (Go's `internal/catalog`).
//!
//! Go walks its component map in random order, so its JSON lists components and
//! edges in a different order on every run. This port sorts components by id.

use std::collections::BTreeMap;
use std::sync::LazyLock;

use hcl::Expression;
use regex::Regex;

use crate::gojson::Value;
use crate::hclx;

/// A component: the stacks grouped under one id and the paths they watch.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Component {
    /// `nonprod/am` for `stacks/nonprod/am/x`, `a/b` for `a/b/c`, else the stack itself.
    pub id: String,
    /// Its stacks, in `terramate list` order.
    pub stacks: Vec<String>,
    /// Its stacks' watch paths, de-duplicated in first-seen order.
    pub watches: Vec<String>,
}

/// A directed edge between components.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Edge {
    /// Source component.
    pub from: String,
    /// Target component.
    pub to: String,
    /// `watch` or `dependency`.
    pub kind: String,
}

/// The whole catalog.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Catalog {
    /// Components, sorted by id.
    pub components: Vec<Component>,
    /// Watch edges, then dependency edges.
    pub edges: Vec<Edge>,
}

/// A stack-level dependency from `terramate experimental run-graph`.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct StackEdge {
    /// The stack that runs first.
    pub from: String,
    /// The stack that runs after it.
    pub to: String,
}

/// The component a stack belongs to (Go's `getComponent`).
pub fn component_of(stack: &str) -> String {
    let parts: Vec<&str> = stack.split('/').collect();
    if parts.len() > 3 && parts[0] == "stacks" {
        parts[1..3].join("/")
    } else if parts.len() > 2 {
        parts[..2].join("/")
    } else {
        stack.to_string()
    }
}

/// The `watch` list of the first `stack {}` block in a `stack.tm.hcl`-style file.
/// Only literal strings are kept, as in Go.
pub fn parse_watches(src: &str) -> Result<Vec<String>, String> {
    let body = hclx::parse(src)?;
    for blk in body.blocks().filter(|b| b.identifier.as_str() == "stack") {
        for attr in blk.body.attributes() {
            if attr.key.as_str() != "watch" {
                continue;
            }
            let Expression::Array(items) = &attr.expr else {
                continue;
            };
            let watches = items
                .iter()
                .filter_map(|e| match e {
                    Expression::String(s) => Some(s.clone()),
                    _ => None,
                })
                .collect();
            return Ok(watches);
        }
    }
    Ok(Vec::new())
}

static NODE_RE: LazyLock<Regex> = LazyLock::new(|| {
    #[expect(
        clippy::expect_used,
        reason = "a constant pattern; covered by the tests"
    )]
    crate::goregex::compile(r#"(\w+)\s*\[label="([^"]*)"\]"#).expect("valid node pattern")
});
static EDGE_RE: LazyLock<Regex> = LazyLock::new(|| {
    #[expect(
        clippy::expect_used,
        reason = "a constant pattern; covered by the tests"
    )]
    crate::goregex::compile(r"(\w+)\s*->\s*(\w+)").expect("valid edge pattern")
});

/// Parses `terramate experimental run-graph -l stack.dir` DOT output into edges,
/// stripping the leading `/` from node labels (Go's `parseRunGraph`).
pub fn parse_run_graph(dot: &str) -> Vec<StackEdge> {
    let mut label = BTreeMap::new();
    for m in NODE_RE.captures_iter(dot) {
        let l = &m[2];
        label.insert(
            m[1].to_string(),
            l.strip_prefix('/').unwrap_or(l).to_string(),
        );
    }
    EDGE_RE
        .captures_iter(dot)
        .filter_map(|m| {
            Some(StackEdge {
                from: label.get(&m[1])?.clone(),
                to: label.get(&m[2])?.clone(),
            })
        })
        .collect()
}

fn dedup(list: &[String]) -> Vec<String> {
    let mut out: Vec<String> = Vec::new();
    for v in list {
        if !out.contains(v) {
            out.push(v.clone());
        }
    }
    out
}

/// Go's `filepath.Base` for a slash path.
fn base(p: &str) -> &str {
    let t = p.trim_end_matches('/');
    if t.is_empty() {
        return if p.is_empty() { "." } else { "/" };
    }
    t.rsplit('/').next().unwrap_or(t)
}

/// Builds the catalog from `terramate list`, each stack's watches (by stack, in
/// list order) and the run-graph edges.
pub fn build(
    stacks: &[String],
    watches: &dyn Fn(&str) -> Vec<String>,
    run_graph: &[StackEdge],
) -> Catalog {
    let mut groups: BTreeMap<String, Vec<String>> = BTreeMap::new();
    let mut watch_map: BTreeMap<String, Vec<String>> = BTreeMap::new();
    for s in stacks {
        let comp = component_of(s);
        groups.entry(comp.clone()).or_default().push(s.clone());
        let w = watches(s);
        if !w.is_empty() {
            watch_map.entry(comp).or_default().extend(w);
        }
    }
    let components: Vec<Component> = groups
        .into_iter()
        .map(|(id, stacks)| Component {
            watches: dedup(watch_map.get(&id).map(Vec::as_slice).unwrap_or_default()),
            id,
            stacks,
        })
        .collect();

    let mut edges: Vec<Edge> = Vec::new();
    let mut push = |e: Edge| {
        if !edges.contains(&e) {
            edges.push(e);
        }
    };
    for comp in &components {
        for w in &comp.watches {
            for other in &components {
                if other.id == comp.id {
                    continue;
                }
                if w.contains(base(&other.id)) {
                    push(Edge {
                        from: other.id.clone(),
                        to: comp.id.clone(),
                        kind: "watch".into(),
                    });
                }
            }
        }
    }
    for e in run_graph {
        let (from, to) = (component_of(&e.from), component_of(&e.to));
        if from.is_empty() || to.is_empty() || from == to {
            continue;
        }
        push(Edge {
            from,
            to,
            kind: "dependency".into(),
        });
    }
    Catalog { components, edges }
}

/// The catalog as Go marshals it: nil slices are `null`.
pub fn to_json(c: &Catalog) -> Value {
    let strings_or_null = |v: &[String]| {
        if v.is_empty() {
            Value::Null
        } else {
            Value::strings(v)
        }
    };
    let components = if c.components.is_empty() {
        Value::Null
    } else {
        Value::Array(
            c.components
                .iter()
                .map(|comp| {
                    Value::Object(vec![
                        ("id".into(), Value::str(&comp.id)),
                        ("stacks".into(), strings_or_null(&comp.stacks)),
                        ("watches".into(), strings_or_null(&comp.watches)),
                    ])
                })
                .collect(),
        )
    };
    let edges = if c.edges.is_empty() {
        Value::Null
    } else {
        Value::Array(
            c.edges
                .iter()
                .map(|e| {
                    Value::Object(vec![
                        ("from".into(), Value::str(&e.from)),
                        ("to".into(), Value::str(&e.to)),
                        ("kind".into(), Value::str(&e.kind)),
                    ])
                })
                .collect(),
        )
    };
    Value::Object(vec![
        ("components".into(), components),
        ("edges".into(), edges),
    ])
}

#[cfg(test)]
mod tests;
