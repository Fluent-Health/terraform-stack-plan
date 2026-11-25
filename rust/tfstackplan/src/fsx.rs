//! Filesystem helpers shared by the faces.

use std::path::{Path, PathBuf};

use tfstackplan_cli::config::{self, Config};

/// Go's `config.Discover`: the nearest `.tfstackplan.hcl` at or above `dir`,
/// stopping at the first ancestor that holds a `.git` entry.
pub fn discover_config(dir: &Path) -> Option<PathBuf> {
    let mut d = std::path::absolute(dir).unwrap_or_else(|_| dir.to_path_buf());
    loop {
        let p = d.join(config::DEFAULT_FILENAME);
        if p.metadata().is_ok_and(|m| m.is_file()) {
            return Some(p);
        }
        if d.join(".git").exists() {
            return None;
        }
        if !d.pop() {
            return None;
        }
    }
}

/// Go's `config.Load`: read and parse. A read error reads like Go's
/// (`open <path>: no such file or directory`).
pub fn load_config(path: &Path) -> Result<Config, String> {
    let src = std::fs::read_to_string(path).map_err(|e| os_error("open", path, &e))?;
    config::parse(&src, &path.to_string_lossy())
}

/// An `*os.PathError` message for a `Path` (`gofs::path_error` does the wording).
pub fn os_error(op: &str, path: &Path, e: &std::io::Error) -> String {
    crate::gofs::path_error(op, &path.to_string_lossy(), e)
}

#[cfg(test)]
mod tests;
