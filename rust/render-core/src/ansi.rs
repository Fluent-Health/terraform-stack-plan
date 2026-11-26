//! Strips ANSI SGR escape sequences from captured command output (`internal/ansi`).

use std::sync::OnceLock;

/// Removes `ESC [ <params> m` sequences, leaving every other byte.
pub fn strip(s: &str) -> String {
    static RE: OnceLock<regex::Regex> = OnceLock::new();
    RE.get_or_init(|| regex::Regex::new(r"\x1b\[[0-9;]*m").unwrap_or_else(|_| unreachable!()))
        .replace_all(s, "")
        .into_owned()
}

#[cfg(test)]
mod tests;
