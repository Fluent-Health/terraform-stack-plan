//! The component catalog (Go: `internal/catalog`): stacks from `terramate list`
//! grouped into components, their watch lists from each stack's HCL, and
//! component edges from watches and `terramate experimental run-graph`.

use std::collections::{HashMap, HashSet};
use std::path::Path;
use std::sync::LazyLock;

use regex::Regex;
use serde::Serialize;

#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub(crate) struct Component {
    pub id: String,
    pub stacks: Vec<String>,
    pub watches: Option<Vec<String>>,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub(crate) struct Edge {
    pub from: String,
    pub to: String,
    pub kind: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub(crate) struct Catalog {
    pub components: Option<Vec<Component>>,
    pub edges: Option<Vec<Edge>>,
}

/// Runs terramate in `dir`; the error text is Go's `runner.Terramate.output`.
async fn terramate(dir: &str, args: &[&str]) -> Result<String, String> {
    let out = tokio::process::Command::new("terramate")
        .args(args)
        .current_dir(dir)
        .output()
        .await
        .map_err(|e| {
            let why = if e.kind() == std::io::ErrorKind::NotFound {
                "exec: \"terramate\": executable file not found in $PATH".to_string()
            } else {
                e.to_string()
            };
            format!("terramate {}: {why}: ", args.join(" "))
        })?;
    if !out.status.success() {
        let code = out.status.code().unwrap_or(-1);
        return Err(format!(
            "terramate {}: exit status {code}: {}",
            args.join(" "),
            String::from_utf8_lossy(&out.stderr)
        ));
    }
    Ok(String::from_utf8_lossy(&out.stdout).into_owned())
}

fn lines(s: &str) -> Vec<String> {
    s.split('\n')
        .map(str::trim)
        .filter(|l| !l.is_empty())
        .map(str::to_string)
        .collect()
}

/// The component of a stack path: `stacks/<a>/<b>/...` groups by `<a>/<b>`, any
/// other path of three or more segments by its first two.
pub(crate) fn component_of(stack: &str) -> String {
    let parts: Vec<&str> = stack.split('/').collect();
    if parts.len() > 3 && parts[0] == "stacks" {
        parts[1..3].join("/")
    } else if parts.len() > 2 {
        parts[..2].join("/")
    } else {
        stack.to_string()
    }
}

/// The literal `watch` list of the first `stack` block of an HCL file.
pub(crate) fn parse_watches(path: &Path) -> Result<Option<Vec<String>>, String> {
    let src = std::fs::read_to_string(path).map_err(|e| e.to_string())?;
    let body = hcl::parse(&src).map_err(|e| e.to_string())?;
    for blk in body.blocks().filter(|b| b.identifier() == "stack") {
        for attr in blk.body().attributes().filter(|a| a.key() == "watch") {
            let hcl::Expression::Array(items) = attr.expr() else {
                continue;
            };
            let watches: Vec<String> = items
                .iter()
                .filter_map(|e| match e {
                    hcl::Expression::String(s) => Some(s.clone()),
                    _ => None,
                })
                .collect();
            return Ok((!watches.is_empty()).then_some(watches));
        }
    }
    Ok(None)
}

static NODE_RE: LazyLock<Option<Regex>> =
    LazyLock::new(|| Regex::new(r#"(\w+)\s*\[label="([^"]*)"\]"#).ok());
static EDGE_RE: LazyLock<Option<Regex>> = LazyLock::new(|| Regex::new(r"(\w+)\s*->\s*(\w+)").ok());

/// Go's `parseRunGraph`: DOT node labels and edges.
pub(crate) fn parse_run_graph(dot: &str) -> Vec<(String, String)> {
    let (Some(node_re), Some(edge_re)) = (NODE_RE.as_ref(), EDGE_RE.as_ref()) else {
        return Vec::new();
    };
    let label: HashMap<&str, &str> = node_re
        .captures_iter(dot)
        .filter_map(|c| {
            Some((
                c.get(1)?.as_str(),
                c.get(2)?.as_str().trim_start_matches('/'),
            ))
        })
        .collect();
    edge_re
        .captures_iter(dot)
        .filter_map(|c| {
            let from = label.get(c.get(1)?.as_str())?;
            let to = label.get(c.get(2)?.as_str())?;
            Some(((*from).to_string(), (*to).to_string()))
        })
        .collect()
}

fn dedup(list: Vec<String>) -> Option<Vec<String>> {
    let mut seen = HashSet::new();
    let out: Vec<String> = list
        .into_iter()
        .filter(|v| seen.insert(v.clone()))
        .collect();
    (!out.is_empty()).then_some(out)
}

/// Builds the catalog of `dir`. Go ranges over maps, so component and edge order
/// is unspecified there; here components follow first appearance.
pub(crate) async fn build(dir: &str) -> Result<Catalog, String> {
    let stacks = lines(
        &terramate(dir, &["list"])
            .await
            .map_err(|e| format!("list stacks: {e}"))?,
    );
    let mut order: Vec<String> = Vec::new();
    let mut groups: HashMap<String, Vec<String>> = HashMap::new();
    let mut watches: HashMap<String, Vec<String>> = HashMap::new();
    for s in &stacks {
        let comp = component_of(s);
        if !groups.contains_key(&comp) {
            order.push(comp.clone());
        }
        groups.entry(comp.clone()).or_default().push(s.clone());
        let stack_dir = Path::new(dir).join(s);
        for file in ["stack.tm.hcl", "terramate.tm.hcl", ".tm.hcl"] {
            if let Ok(Some(w)) = parse_watches(&stack_dir.join(file)) {
                watches.entry(comp.clone()).or_default().extend(w);
                break;
            }
        }
    }
    let components: Vec<Component> = order
        .iter()
        .map(|id| Component {
            id: id.clone(),
            stacks: groups.remove(id).unwrap_or_default(),
            watches: dedup(watches.remove(id).unwrap_or_default()),
        })
        .collect();
    let raw_edges = terramate(dir, &["experimental", "run-graph", "-l", "stack.dir"])
        .await
        .map(|dot| parse_run_graph(&dot))
        .unwrap_or_default();
    let mut seen = HashSet::new();
    let mut edges = Vec::new();
    for comp in &components {
        for w in comp.watches.iter().flatten() {
            for other in components.iter().filter(|o| o.id != comp.id) {
                let other_name = Path::new(&other.id)
                    .file_name()
                    .map(|n| n.to_string_lossy().into_owned())
                    .unwrap_or_default();
                if w.contains(&other_name) && seen.insert(format!("{}:{}:watch", other.id, comp.id))
                {
                    edges.push(Edge {
                        from: other.id.clone(),
                        to: comp.id.clone(),
                        kind: "watch".into(),
                    });
                }
            }
        }
    }
    for (from, to) in raw_edges {
        let (fc, tc) = (component_of(&from), component_of(&to));
        if fc.is_empty() || tc.is_empty() || fc == tc {
            continue;
        }
        if seen.insert(format!("{fc}:{tc}:dependency")) {
            edges.push(Edge {
                from: fc,
                to: tc,
                kind: "dependency".into(),
            });
        }
    }
    Ok(Catalog {
        components: (!components.is_empty()).then_some(components),
        edges: (!edges.is_empty()).then_some(edges),
    })
}

#[cfg(test)]
mod tests;
