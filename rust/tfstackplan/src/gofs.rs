//! File system calls and path handling with Go's semantics and error text, so errors read
//! exactly like the Go binary's (`open x: no such file or directory`).

use std::fs;
use std::io::{self, Read, Write};
use std::path::Path;

/// Go's text for an OS error: the C library message with a lower-case first letter, which is
/// what Go's `syscall.Errno` table holds on Linux.
pub fn errno_text(e: &io::Error) -> String {
    let msg = match e.raw_os_error() {
        Some(code) => {
            let full = io::Error::from_raw_os_error(code).to_string();
            full.split(" (os error").next().unwrap_or(&full).to_string()
        }
        None => e.to_string(),
    };
    let mut c = msg.chars();
    match c.next() {
        Some(f) => f.to_lowercase().collect::<String>() + c.as_str(),
        None => msg,
    }
}

/// `*fs.PathError` text: `op path: errno`.
pub fn path_error(op: &str, path: &str, e: &io::Error) -> String {
    format!("{op} {path}: {}", errno_text(e))
}

/// `os.ReadFile`.
pub fn read_file(path: &str) -> Result<Vec<u8>, String> {
    let mut f = fs::File::open(path).map_err(|e| path_error("open", path, &e))?;
    let mut buf = Vec::new();
    f.read_to_end(&mut buf)
        .map_err(|e| path_error("read", path, &e))?;
    Ok(buf)
}

/// `os.WriteFile(path, data, 0o644)`.
pub fn write_file(path: &str, data: &[u8]) -> Result<(), String> {
    let mut opts = fs::OpenOptions::new();
    opts.write(true).create(true).truncate(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        opts.mode(0o644);
    }
    let mut f = opts.open(path).map_err(|e| path_error("open", path, &e))?;
    f.write_all(data).map_err(|e| path_error("write", path, &e))
}

/// `filepath.Clean` for slash paths.
pub fn clean(path: &str) -> String {
    if path.is_empty() {
        return ".".to_string();
    }
    let rooted = path.starts_with('/');
    let mut parts: Vec<&str> = Vec::new();
    for p in path.split('/') {
        match p {
            "" | "." => {}
            ".." => {
                if parts.last().is_some_and(|l| *l != "..") {
                    parts.pop();
                } else if !rooted {
                    parts.push("..");
                }
            }
            p => parts.push(p),
        }
    }
    let body = parts.join("/");
    match (rooted, body.is_empty()) {
        (true, _) => format!("/{body}"),
        (false, true) => ".".to_string(),
        (false, false) => body,
    }
}

/// `filepath.Join`.
pub fn join(parts: &[&str]) -> String {
    let nonempty: Vec<&str> = parts.iter().copied().filter(|p| !p.is_empty()).collect();
    if nonempty.is_empty() {
        return String::new();
    }
    clean(&nonempty.join("/"))
}

/// `os.Getwd`: `$PWD` when it names the current directory, else the real path.
pub fn getwd() -> Option<String> {
    let dot = fs::metadata(".").ok()?;
    if let Ok(pwd) = std::env::var("PWD")
        && pwd.starts_with('/')
        && let Ok(m) = fs::metadata(&pwd)
        && same_file(&m, &dot)
    {
        return Some(pwd);
    }
    std::env::current_dir()
        .ok()
        .map(|p| p.to_string_lossy().into_owned())
}

#[cfg(unix)]
fn same_file(a: &fs::Metadata, b: &fs::Metadata) -> bool {
    use std::os::unix::fs::MetadataExt;
    a.dev() == b.dev() && a.ino() == b.ino()
}

#[cfg(not(unix))]
fn same_file(_: &fs::Metadata, _: &fs::Metadata) -> bool {
    false
}

/// `filepath.Abs`.
pub fn abs(path: &str) -> Option<String> {
    if path.starts_with('/') {
        return Some(clean(path));
    }
    Some(join(&[&getwd()?, path]))
}

/// `filepath.Rel` for two absolute, clean paths; `None` where Go errors.
pub fn rel(base: &str, target: &str) -> Option<String> {
    let base = clean(base);
    let target = clean(target);
    if base == target {
        return Some(".".to_string());
    }
    let split = |p: &str| -> Vec<String> {
        if p == "/" {
            Vec::new()
        } else {
            p.trim_start_matches('/')
                .split('/')
                .map(str::to_string)
                .collect()
        }
    };
    if base.starts_with('/') != target.starts_with('/') {
        return None;
    }
    let b = split(&base);
    let t = split(&target);
    let common = b.iter().zip(&t).take_while(|(x, y)| x == y).count();
    if b[common..].iter().any(|p| p == "..") {
        return None;
    }
    let mut out: Vec<&str> = vec![".."; b.len() - common];
    out.extend(t[common..].iter().map(String::as_str));
    Some(out.join("/"))
}

/// `os.Stat(path)` then "is a regular file".
pub fn is_regular_file(path: &str) -> bool {
    fs::metadata(path).is_ok_and(|m| m.is_file())
}

/// `os.Stat(path)` succeeds.
pub fn exists(path: &str) -> bool {
    fs::metadata(path).is_ok()
}

/// Directory entries sorted by name (`os.ReadDir`), each with whether it is a directory
/// (symlinks are not followed).
pub fn read_dir(path: &str) -> Result<Vec<(String, bool)>, io::Error> {
    let mut out = Vec::new();
    for e in fs::read_dir(Path::new(path))? {
        let e = e?;
        let is_dir = e.file_type()?.is_dir();
        out.push((e.file_name().to_string_lossy().into_owned(), is_dir));
    }
    out.sort();
    Ok(out)
}

#[cfg(test)]
mod tests;
