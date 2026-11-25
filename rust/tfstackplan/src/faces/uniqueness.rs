//! `uniqueness`: the `env_uniqueness` lint over every Catalyst instance manifest
//! under `--dir`. Exit 0 clean, 1 unjustified violations or stale allows, 2 a
//! usage, config or load error.

use std::path::{Path, PathBuf};
use std::process::ExitCode;

use tfstackplan_cli::config::SourceBlock;
use tfstackplan_cli::flag::FlagSet;
use tfstackplan_cli::uniqueness::source::{LoadError, load_unit, match_glob, split_glob};
use tfstackplan_cli::uniqueness::{Date, Unit, evaluate, report};

use super::{err, out, parse};
use crate::fsx;

fn fail(msg: &str) -> ExitCode {
    err(&format!("tfstackplan uniqueness: {msg}\n"));
    ExitCode::from(2)
}

/// Runs `uniqueness`.
pub fn run(args: &[String]) -> ExitCode {
    let mut fs = FlagSet::new("uniqueness");
    fs.string(
        "dir",
        ".",
        "repo root to scan for Catalyst instance manifests",
    )
    .string(
        "config",
        "",
        "HCL config (default: auto-discover .tfstackplan.hcl under --dir)",
    )
    .string("format", "text", "output format: text|json");
    let p = match parse(&fs, args) {
        Ok(p) => p,
        Err(code) => return code,
    };
    let dir = p.str("dir");

    let resolved = if p.str("config").is_empty() {
        match fsx::discover_config(Path::new(dir)) {
            Some(path) => path,
            None => {
                return fail(&format!(
                    "no .tfstackplan.hcl found under {dir} (pass --config)"
                ));
            }
        }
    } else {
        PathBuf::from(p.str("config"))
    };
    let cfg = match fsx::load_config(&resolved) {
        Ok(c) => c,
        Err(e) => return fail(&e),
    };
    let Some(eu) = cfg.env_uniqueness else {
        return fail(&format!(
            "no env_uniqueness block in {}",
            resolved.display()
        ));
    };

    let units = match load_units(dir, &eu.source) {
        Ok(u) => u,
        Err(e) => return fail(&e),
    };
    let rep = match evaluate(&eu, &units, today()) {
        Ok(r) => r,
        Err(e) => return fail(&e),
    };

    match p.str("format") {
        "text" => out(&report::render_text(&rep)),
        "json" => match report::render_json(&rep) {
            Ok(s) => out(&s),
            Err(e) => return fail(&e),
        },
        other => {
            return fail(&format!(
                "--format must be text|json, got {}",
                tfstackplan_cli::gofmt::quote(other)
            ));
        }
    }
    if !rep.unjustified.is_empty() || !rep.stale.is_empty() {
        return ExitCode::from(1);
    }
    ExitCode::SUCCESS
}

/// Today's date in UTC. Go uses the local date; the two differ only when the
/// local zone is on the other side of midnight.
fn today() -> Date {
    let secs = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| i64::try_from(d.as_secs()).unwrap_or(i64::MAX))
        .unwrap_or(0);
    Date::from_unix(secs)
}

/// Go's `LoadUnits`: walk `root`, keep files matching the source glob, parse each.
fn load_units(root: &str, src: &SourceBlock) -> Result<Vec<Unit>, String> {
    let glob = if src.glob.is_empty() {
        tfstackplan_cli::config::DEFAULT_SOURCE_GLOB
    } else {
        &src.glob
    };
    let env_path = if src.environments_path.is_empty() {
        tfstackplan_cli::config::DEFAULT_SOURCE_ENVIRONMENTS_PATH
    } else {
        &src.environments_path
    };
    let inputs_path = if src.inputs_path.is_empty() {
        tfstackplan_cli::config::DEFAULT_SOURCE_INPUTS_PATH
    } else {
        &src.inputs_path
    };
    let (prefix, suffix) = split_glob(glob)?;

    let mut files = Vec::new();
    walk(Path::new(root), "", &mut files)
        .map_err(|e| format!("env_uniqueness: walking {root}: {e}"))?;
    let mut paths = Vec::new();
    for rel in files {
        if match_glob(&rel, &prefix, &suffix)
            .map_err(|e| format!("env_uniqueness: walking {root}: {e}"))?
        {
            paths.push(rel);
        }
    }
    paths.sort();

    let mut units = Vec::new();
    for rel in paths {
        let full = Path::new(root).join(&rel);
        let data = std::fs::read(&full).map_err(|e| {
            format!(
                "env_uniqueness: reading {rel}: {}",
                fsx::os_error("open", &full, &e)
            )
        })?;
        let content = String::from_utf8_lossy(&data);
        match load_unit(&rel, &content, &prefix, env_path, inputs_path) {
            Ok(u) => units.push(u),
            Err(LoadError::NoEnvironments) => {}
            Err(LoadError::Fatal(e)) => return Err(e),
        }
    }
    Ok(units)
}

/// `filepath.WalkDir`: every non-directory entry (symlinks are not followed) as
/// a root-relative slash path. Errors read like Go's.
fn walk(root: &Path, rel: &str, out: &mut Vec<String>) -> Result<(), String> {
    let dir = if rel.is_empty() {
        root.to_path_buf()
    } else {
        root.join(rel)
    };
    if rel.is_empty() {
        let meta = std::fs::symlink_metadata(&dir).map_err(|e| fsx::os_error("lstat", &dir, &e))?;
        if !meta.is_dir() {
            out.push(".".to_string());
            return Ok(());
        }
    }
    let rd = std::fs::read_dir(&dir).map_err(|e| fsx::os_error("open", &dir, &e))?;
    let mut entries: Vec<(String, bool)> = Vec::new();
    for entry in rd {
        let entry = entry.map_err(|e| fsx::os_error("readdirent", &dir, &e))?;
        let is_dir = entry.file_type().map(|t| t.is_dir()).unwrap_or(false);
        entries.push((entry.file_name().to_string_lossy().into_owned(), is_dir));
    }
    entries.sort();
    for (name, is_dir) in entries {
        let child = if rel.is_empty() {
            name
        } else {
            format!("{rel}/{name}")
        };
        if is_dir {
            walk(root, &child, out)?;
        } else {
            out.push(child);
        }
    }
    Ok(())
}

#[cfg(test)]
mod tests;
