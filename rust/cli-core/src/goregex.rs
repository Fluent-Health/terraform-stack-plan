//! Go RE2 patterns on the `regex` crate, through `tfstackplan_render::goregex`: Go's ASCII
//! `\d`, `\w`, `\s` and `\b`, and Go's wording for syntax errors.

use regex::Regex;
use tfstackplan_render::goregex::GoRegex;

/// Compiles a Go regexp; the error reads like Go's
/// (`error parsing regexp: missing closing ]: `[x``).
pub fn compile(pattern: &str) -> Result<Regex, String> {
    GoRegex::new(pattern).map(GoRegex::into_regex)
}

#[cfg(test)]
mod tests;
