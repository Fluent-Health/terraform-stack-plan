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

/// An `*os.PathError` message: `<op> <path>: <errno text>`, worded like Go's.
pub fn os_error(op: &str, path: &Path, e: &std::io::Error) -> String {
    format!("{op} {}: {}", path.display(), errno_text(e))
}

/// The lower-case errno text Go prints (`no such file or directory`), falling
/// back to Rust's description.
pub fn errno_text(e: &std::io::Error) -> String {
    use std::io::ErrorKind::*;
    match e.kind() {
        NotFound => "no such file or directory".into(),
        PermissionDenied => "permission denied".into(),
        IsADirectory => "is a directory".into(),
        NotADirectory => "not a directory".into(),
        AlreadyExists => "file exists".into(),
        DirectoryNotEmpty => "directory not empty".into(),
        _ => {
            let s = e.to_string();
            // Rust appends " (os error N)"; Go does not.
            match s.find(" (os error") {
                Some(i) => {
                    let t = &s[..i];
                    let mut c = t.chars();
                    match c.next() {
                        Some(f) => f.to_lowercase().collect::<String>() + c.as_str(),
                        None => String::new(),
                    }
                }
                None => s,
            }
        }
    }
}

#[cfg(test)]
mod tests;
