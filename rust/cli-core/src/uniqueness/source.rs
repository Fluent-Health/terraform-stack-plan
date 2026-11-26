//! The pure half of `LoadUnits`: the `<dir>/**/<pattern>` glob, and turning one
//! manifest's text into a [`Unit`]. The shell walks the tree and reads files.

use super::{Unit, Value, flatten};
use crate::glob;
use crate::gofmt::quote;

/// Splits `<prefix>/**/<suffix-glob>` into `("<prefix>/", "<suffix-glob>")`.
pub fn split_glob(glob: &str) -> Result<(String, String), String> {
    match glob.split_once("/**/") {
        Some((p, s)) if !p.is_empty() && !s.is_empty() => Ok((format!("{p}/"), s.to_string())),
        _ => Err(format!(
            "env_uniqueness: source glob {} must have the <dir>/**/<pattern> shape",
            quote(glob)
        )),
    }
}

/// Whether a root-relative slash path is under `prefix` and its last segments
/// match `suffix_glob` (any directories may sit between, standing in for `**`).
pub fn match_glob(rel: &str, prefix: &str, suffix_glob: &str) -> Result<bool, glob::BadPattern> {
    let Some(tail) = rel.strip_prefix(prefix) else {
        return Ok(false);
    };
    let tail_parts: Vec<&str> = tail.split('/').collect();
    let n = suffix_glob.split('/').count();
    if tail_parts.len() < n {
        return Ok(false);
    }
    glob::matches(suffix_glob, &tail_parts[tail_parts.len() - n..].join("/"))
}

/// Why a manifest produced no unit.
#[derive(Debug, PartialEq, Eq)]
pub enum LoadError {
    /// No environments block at all: a single-env instance, skipped.
    NoEnvironments,
    /// Anything else: fatal, with Go's message.
    Fatal(String),
}

fn navigate<'a>(doc: &'a [(String, Value)], path: &str) -> Option<&'a Value> {
    let mut parts = path.split('.');
    let first = parts.next()?;
    let mut cur = doc.iter().find(|(k, _)| k == first).map(|(_, v)| v)?;
    for part in parts {
        let Value::Map(m) = cur else { return None };
        cur = m.iter().find(|(k, _)| k == part).map(|(_, v)| v)?;
    }
    Some(cur)
}

/// Parses one manifest (`rel` is its root-relative slash path) into a unit.
pub fn load_unit(
    rel: &str,
    content: &str,
    prefix: &str,
    env_path: &str,
    inputs_path: &str,
) -> Result<Unit, LoadError> {
    let doc = super::yaml::unmarshal_map(content)
        .map_err(|e| LoadError::Fatal(format!("env_uniqueness: parsing {rel}: {e}")))?
        .unwrap_or_default();
    let envs_raw = navigate(&doc, env_path).ok_or(LoadError::NoEnvironments)?;
    let Value::Map(envs_map) = envs_raw else {
        return Err(LoadError::Fatal(format!(
            "env_uniqueness: {rel}: {} is not a map",
            quote(env_path)
        )));
    };
    let trimmed = rel.strip_prefix(prefix).unwrap_or(rel);
    let mut unit = Unit {
        id: trimmed
            .strip_suffix(".tm.yml")
            .unwrap_or(trimmed)
            .to_string(),
        ..Default::default()
    };
    for (env, block) in envs_map {
        let inputs = navigate_inputs(block, inputs_path).map_err(|e| {
            LoadError::Fatal(format!(
                "env_uniqueness: {rel}: environment {}: {e}",
                quote(env)
            ))
        })?;
        unit.inputs.insert(env.clone(), flatten(&inputs));
        unit.envs.push(env.clone());
    }
    unit.envs.sort();
    Ok(unit)
}

fn navigate_inputs(block: &Value, inputs_path: &str) -> Result<Vec<(String, Value)>, String> {
    let m = match block {
        Value::Nil => return Ok(Vec::new()),
        Value::Map(m) => m,
        other => return Err(format!("block is not a map ({})", other.go_type())),
    };
    match navigate(m, inputs_path) {
        None | Some(Value::Nil) => Ok(Vec::new()),
        Some(Value::Map(inputs)) => Ok(inputs.clone()),
        Some(other) => Err(format!(
            "{} is not a map ({})",
            quote(inputs_path),
            other.go_type()
        )),
    }
}

#[cfg(test)]
mod tests;
