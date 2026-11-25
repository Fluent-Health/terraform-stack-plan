//! Finds where a plan resource is declared, so it can link to file:line (`internal/source`).
//! The caller reads the files; this module parses them and keeps the index.

use std::collections::HashMap;

use hcl::edit::Span;
use hcl::edit::structure::Structure;

/// A resource declaration: file relative to the repo root, and line.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Loc {
    /// Slash-separated path relative to the repo root.
    pub file: String,
    /// 1-based line of the `resource` keyword.
    pub line: usize,
}

/// (module key, type, name) to declaration.
#[derive(Debug, Clone, Default)]
pub struct Index {
    m: HashMap<(String, String, String), Loc>,
}

/// The `resource "<type>" "<name>"` blocks in one `.tf` file, with the line each starts on, or
/// `None` when the file does not parse.
pub fn resource_blocks(text: &str) -> Option<Vec<(String, String, usize)>> {
    let body = hcl::edit::parser::parse_body(text).ok()?;
    let mut out = Vec::new();
    for s in body.iter() {
        let Structure::Block(b) = s else { continue };
        if b.ident.as_str() != "resource" || b.labels.len() != 2 {
            continue;
        }
        let start = b.ident.span().map_or(0, |r| r.start);
        let line = text[..start.min(text.len())].matches('\n').count() + 1;
        out.push((b.labels[0].as_str().to_string(), b.labels[1].as_str().to_string(), line));
    }
    Some(out)
}

impl Index {
    /// Records a file's resources under a module key; the first declaration wins.
    pub fn add_file(&mut self, module_key: &str, rel_file: &str, resources: Vec<(String, String, usize)>) {
        for (typ, name, line) in resources {
            self.m
                .entry((module_key.to_string(), typ, name))
                .or_insert_with(|| Loc {
                    file: rel_file.to_string(),
                    line,
                });
        }
    }

    /// Resolves a plan resource. `module_address` is the plan's (`""` for root,
    /// `module.a.module.b[0]` for nested); instance keys are ignored.
    pub fn lookup(&self, module_address: &str, typ: &str, name: &str) -> Option<&Loc> {
        self.m
            .get(&(module_key(module_address), typ.to_string(), name.to_string()))
    }
}

/// Turns a plan module address (`module.a.module.b[0]`) into the modules.json key (`a.b`).
pub fn module_key(module_address: &str) -> String {
    if module_address.is_empty() {
        return String::new();
    }
    let parts: Vec<&str> = module_address.split('.').collect();
    let mut keys = Vec::new();
    let mut i = 0;
    while i < parts.len() {
        if parts[i] == "module" && i + 1 < parts.len() {
            let name = parts[i + 1];
            keys.push(name.split('[').next().unwrap_or(name));
            i += 1;
        }
        i += 1;
    }
    keys.join(".")
}

/// A module directory cached under `.terraform` (skipped: not our source).
pub fn under_dot_terraform(dir: &str) -> bool {
    dir == ".terraform" || dir.starts_with(".terraform/") || dir.contains("/.terraform/")
}

/// The (key, dir) of every module in `.terraform/modules/modules.json`, or `None` when it does
/// not decode. Field names match case-insensitively, as Go's decoder does.
pub fn parse_modules_json(data: &[u8]) -> Option<Vec<(String, String)>> {
    crate::gojson::check_valid(data).ok()?;
    let doc: serde_json::Value = serde_json::from_str(&String::from_utf8_lossy(data)).ok()?;
    let field = |o: &serde_json::Map<String, serde_json::Value>, name: &str| -> Option<serde_json::Value> {
        o.get(name)
            .or_else(|| o.iter().find(|(k, _)| k.eq_ignore_ascii_case(name)).map(|(_, v)| v))
            .cloned()
    };
    let text = |v: Option<serde_json::Value>| -> Option<String> {
        match v {
            None | Some(serde_json::Value::Null) => Some(String::new()),
            Some(serde_json::Value::String(s)) => Some(s),
            _ => None,
        }
    };
    let modules = match &doc {
        serde_json::Value::Null => return Some(Vec::new()),
        serde_json::Value::Object(o) => field(o, "Modules"),
        _ => return None,
    };
    let mut out = Vec::new();
    match modules {
        None | Some(serde_json::Value::Null) => {}
        Some(serde_json::Value::Array(items)) => {
            for m in &items {
                match m {
                    serde_json::Value::Object(o) => {
                        text(field(o, "Source"))?;
                        out.push((text(field(o, "Key"))?, text(field(o, "Dir"))?));
                    }
                    serde_json::Value::Null => out.push((String::new(), String::new())),
                    _ => return None,
                }
            }
        }
        Some(_) => return None,
    }
    Some(out)
}

#[cfg(test)]
mod tests;
