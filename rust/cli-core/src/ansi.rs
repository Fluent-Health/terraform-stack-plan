//! `internal/ansi`: strip SGR colour codes from terminal output.

use std::sync::LazyLock;

use regex::Regex;

static SGR: LazyLock<Regex> = LazyLock::new(|| {
    #[expect(
        clippy::expect_used,
        reason = "a constant pattern; covered by the tests"
    )]
    Regex::new("\x1b\\[[0-9;]*m").expect("valid SGR pattern")
});

/// Removes `ESC [ … m` sequences.
pub fn strip(s: &str) -> String {
    SGR.replace_all(s, "").into_owned()
}

#[cfg(test)]
mod tests;
