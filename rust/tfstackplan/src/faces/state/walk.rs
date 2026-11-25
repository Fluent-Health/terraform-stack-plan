//! Walking a terramate tree for the reserved `_tfsp_move.*` / `_tfsp_xmove.*`
//! files, in Go's `filepath.WalkDir` order (lexical, symlinks not followed).

use std::path::Path;

use tfstackplan_cli::statemove::manifest::{FoundShim, FoundXMove};
use tfstackplan_cli::statemove::{
    parse_shim, parse_xmove, paths, shim_key_from_file, xmove_key_from_file,
};

use crate::fsx::os_error;

/// A file in our namespace: its path (as `WalkDir` builds it), the directory
/// holding it relative to the root (`.` for the root) and its filename key.
struct Hit {
    path: String,
    rel_dir: String,
    key: String,
}

fn walk(root: &str, key_of: fn(&str) -> Option<&str>) -> Result<Vec<Hit>, String> {
    let meta =
        std::fs::symlink_metadata(root).map_err(|e| os_error("lstat", Path::new(root), &e))?;
    let mut out = Vec::new();
    if !meta.is_dir() {
        let name = Path::new(root)
            .file_name()
            .map(|n| n.to_string_lossy().into_owned())
            .unwrap_or_default();
        if let Some(k) = key_of(&name) {
            out.push(Hit {
                path: root.to_string(),
                rel_dir: ".".to_string(),
                key: k.to_string(),
            });
        }
        return Ok(out);
    }
    walk_dir(root, &[], key_of, &mut out)?;
    Ok(out)
}

fn walk_dir(
    dir: &str,
    rel: &[String],
    key_of: fn(&str) -> Option<&str>,
    out: &mut Vec<Hit>,
) -> Result<(), String> {
    let rd = std::fs::read_dir(dir).map_err(|e| os_error("open", Path::new(dir), &e))?;
    let mut entries: Vec<(String, bool)> = Vec::new();
    for e in rd {
        let e = e.map_err(|e| os_error("readdirent", Path::new(dir), &e))?;
        let is_dir = e.file_type().map(|t| t.is_dir()).unwrap_or(false);
        entries.push((e.file_name().to_string_lossy().into_owned(), is_dir));
    }
    entries.sort();
    for (name, is_dir) in entries {
        let path = paths::join(&[dir, &name]);
        if is_dir {
            let mut sub = rel.to_vec();
            sub.push(name);
            walk_dir(&path, &sub, key_of, out)?;
        } else if let Some(k) = key_of(&name) {
            out.push(Hit {
                path,
                rel_dir: if rel.is_empty() {
                    ".".to_string()
                } else {
                    rel.join("/")
                },
                key: k.to_string(),
            });
        }
    }
    Ok(())
}

fn read(path: &str) -> Result<String, String> {
    std::fs::read_to_string(path).map_err(|e| os_error("open", Path::new(path), &e))
}

/// Go's `statemove.Discover`: every shim under `root`, fail-closed on a corrupt
/// or key-mismatched file.
pub fn discover(root: &str) -> Result<Vec<FoundShim>, String> {
    let mut out = Vec::new();
    for h in walk(root, shim_key_from_file)? {
        let data = read(&h.path)?;
        let (pk, ops) =
            parse_shim(&data).map_err(|e| format!("state-move shim {}: {e}", h.path))?;
        if pk != h.key {
            return Err(format!(
                "state-move shim {}: key mismatch (filename {} != header {})",
                h.path,
                tfstackplan_cli::gofmt::quote(&h.key),
                tfstackplan_cli::gofmt::quote(&pk)
            ));
        }
        out.push(FoundShim {
            stack: h.rel_dir,
            key: h.key,
            ops,
        });
    }
    Ok(out)
}

/// Go's `statemove.DiscoverXMoves`: every xmove manifest under `root`. A missing
/// header key is taken from the filename; a different one is an error.
pub fn discover_xmoves(root: &str) -> Result<Vec<FoundXMove>, String> {
    let mut out = Vec::new();
    for h in walk(root, xmove_key_from_file)? {
        let data = read(&h.path)?;
        let (key, xmove) =
            parse_xmove(&data).map_err(|e| format!("xmove manifest {}: {e}", h.path))?;
        if !key.is_empty() && key != h.key {
            return Err(format!(
                "xmove manifest {}: key mismatch (filename {} != header {})",
                h.path,
                tfstackplan_cli::gofmt::quote(&h.key),
                tfstackplan_cli::gofmt::quote(&key)
            ));
        }
        out.push(FoundXMove {
            path: h.path,
            dest_stack: h.rel_dir,
            key: h.key,
            xmove,
        });
    }
    Ok(out)
}

fn cleanup_by(root: &str, key: &str, key_of: fn(&str) -> Option<&str>) -> Result<usize, String> {
    let hits: Vec<Hit> = walk(root, key_of)?
        .into_iter()
        .filter(|h| key.is_empty() || h.key == key)
        .collect();
    for h in &hits {
        std::fs::remove_file(&h.path).map_err(|e| os_error("remove", Path::new(&h.path), &e))?;
    }
    Ok(hits.len())
}

/// Go's `statemove.Cleanup`: removes shims with filename key `key` (all when
/// empty), without parsing them.
pub fn cleanup(root: &str, key: &str) -> Result<usize, String> {
    cleanup_by(root, key, shim_key_from_file)
}

/// Go's `statemove.CleanupXMoves`.
pub fn cleanup_xmoves(root: &str, key: &str) -> Result<usize, String> {
    cleanup_by(root, key, xmove_key_from_file)
}

/// The contents of a directory's `*.tf` files, in name order (unreadable ones skipped).
pub fn tf_files(dir: &str) -> Vec<String> {
    let Ok(rd) = std::fs::read_dir(dir) else {
        return Vec::new();
    };
    let mut names: Vec<String> = rd
        .filter_map(Result::ok)
        .filter(|e| e.file_type().is_ok_and(|t| !t.is_dir()))
        .map(|e| e.file_name().to_string_lossy().into_owned())
        .filter(|n| n.ends_with(".tf"))
        .collect();
    names.sort();
    names
        .iter()
        .filter_map(|n| std::fs::read_to_string(paths::join(&[dir, n])).ok())
        .collect()
}

#[cfg(test)]
mod tests;
