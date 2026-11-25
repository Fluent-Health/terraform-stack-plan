//! How `claims list` prints a claim.

use std::sync::LazyLock;

use regex::Regex;

static RFC3339: LazyLock<Regex> = LazyLock::new(|| {
    #[expect(clippy::expect_used, reason = "a constant pattern; covered by the tests")]
    Regex::new(r"^([0-9]{4}-[0-9]{2}-[0-9]{2})[Tt]([0-9]{2}:[0-9]{2}:[0-9]{2})(\.[0-9]+)?([Zz]|[+-][0-9]{2}:[0-9]{2})$")
        .expect("valid RFC 3339 pattern")
});

/// Go's `expiresAt.Format("2006-01-02T15:04:05Z")` on a time decoded from RFC
/// 3339: the wall-clock date and time in the timestamp's own offset, seconds
/// precision, and a literal `Z` whatever the offset was. An empty value is Go's
/// zero time.
pub fn format_expiry(rfc3339: &str) -> Result<String, String> {
    if rfc3339.is_empty() {
        return Ok("0001-01-01T00:00:00Z".to_string());
    }
    let m = RFC3339.captures(rfc3339).ok_or_else(|| {
        format!(
            "parsing time {} as \"2006-01-02T15:04:05Z07:00\": cannot parse",
            crate::gofmt::quote(rfc3339)
        )
    })?;
    Ok(format!("{}T{}Z", &m[1], &m[2]))
}

/// One `claims list` line: `  stack=%-40s  pr=#%d  expires=%s`.
pub fn line(stack: &str, pr: i64, expires: &str) -> String {
    format!("  stack={stack:<40}  pr=#{pr}  expires={expires}\n")
}

#[cfg(test)]
mod tests;
