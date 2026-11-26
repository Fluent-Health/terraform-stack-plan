//! Timestamps in SQLite columns, as the Go driver writes and reads them.

use jiff::Timestamp;

use crate::{Error, Result};

/// A timestamp as Go's SQLite driver stores a UTC `time.Time`: its `String()`
/// form, `2006-01-02 15:04:05.999999999 +0000 UTC`. `apply_claims.expires_at` is
/// compared as text (`expires_at <= ?`), so both binaries must write this form.
pub(crate) fn to_sql(ts: Timestamp) -> String {
    format!("{} +0000 UTC", ts.strftime("%Y-%m-%d %H:%M:%S%.f"))
}

/// Parses a timestamp column: Go's `String()` form, RFC 3339, or SQLite's
/// `CURRENT_TIMESTAMP` (`YYYY-MM-DD HH:MM:SS`, UTC).
pub(crate) fn from_sql(raw: &str) -> Result<Timestamp> {
    let bad = || Error::Time(raw.to_string());
    if let Some(go) = raw.strip_suffix(" +0000 UTC") {
        return utc(go).ok_or_else(bad);
    }
    if let Ok(ts) = raw.parse::<Timestamp>() {
        return Ok(ts);
    }
    utc(raw).ok_or_else(bad)
}

/// A civil `YYYY-MM-DD HH:MM:SS[.fff]` read as UTC.
fn utc(civil: &str) -> Option<Timestamp> {
    format!("{}Z", civil.replacen(' ', "T", 1)).parse().ok()
}

#[cfg(test)]
mod tests;
