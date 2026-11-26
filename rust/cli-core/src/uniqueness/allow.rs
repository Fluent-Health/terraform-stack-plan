//! Matching findings against reviewed `allow` exceptions.

use super::{AllowRule, Severity, Violation};
use crate::glob;

/// A calendar date. Go compares against `time.Now()`'s local date; the shell
/// passes today's date in UTC.
#[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord)]
pub struct Date {
    /// Year.
    pub year: i32,
    /// Month, 1-12.
    pub month: u32,
    /// Day, 1-31.
    pub day: u32,
}

impl Date {
    /// The UTC date `secs` seconds after the Unix epoch.
    pub fn from_unix(secs: i64) -> Self {
        // Howard Hinnant's civil_from_days.
        let z = secs.div_euclid(86_400) + 719_468;
        let era = z.div_euclid(146_097);
        let doe = z - era * 146_097;
        let yoe = (doe - doe / 1460 + doe / 36_524 - doe / 146_096) / 365;
        let y = yoe + era * 400;
        let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
        let mp = (5 * doy + 2) / 153;
        let d = doy - (153 * mp + 2) / 5 + 1;
        let m = if mp < 10 { mp + 3 } else { mp - 9 };
        let y = if m <= 2 { y + 1 } else { y };
        Date {
            year: i32::try_from(y).unwrap_or(i32::MAX),
            month: u32::try_from(m).unwrap_or(1),
            day: u32::try_from(d).unwrap_or(1),
        }
    }

    /// `time.Parse("2006-01-02", s)`: exactly `YYYY-MM-DD`, a real day.
    pub fn parse(s: &str) -> Option<Self> {
        let b = s.as_bytes();
        if b.len() != 10
            || b[4] != b'-'
            || b[7] != b'-'
            || ![0, 1, 2, 3, 5, 6, 8, 9]
                .iter()
                .all(|&i| b[i].is_ascii_digit())
        {
            return None;
        }
        let year: i32 = s[..4].parse().ok()?;
        let month: u32 = s[5..7].parse().ok()?;
        let day: u32 = s[8..10].parse().ok()?;
        let leap = (year % 4 == 0 && year % 100 != 0) || year % 400 == 0;
        let dim = match month {
            1 | 3 | 5 | 7 | 8 | 10 | 12 => 31,
            4 | 6 | 9 | 11 => 30,
            2 if leap => 29,
            2 => 28,
            _ => return None,
        };
        if day < 1 || day > dim {
            return None;
        }
        Some(Date { year, month, day })
    }
}

/// Whether an allow has expired as of `today`: an empty date never expires, a
/// date before today has, and a date that does not parse counts as expired.
pub fn is_expired(expires: &str, today: Date) -> bool {
    if expires.is_empty() {
        return false;
    }
    Date::parse(expires).is_none_or(|d| d < today)
}

/// Whether `a` justifies `v`: same unit, key equal or matching as a glob, its
/// envs a superset of the finding's, and not expired.
pub fn allow_matches(a: &AllowRule, v: &Violation, today: Date) -> bool {
    if a.unit != v.unit {
        return false;
    }
    if a.key != v.key && glob::matches(&a.key, &v.key) != Ok(true) {
        return false;
    }
    if !v.envs.iter().all(|e| a.envs.contains(e)) {
        return false;
    }
    !is_expired(&a.expires, today)
}

/// Splits blocking findings into unjustified ones and reports active allows that
/// matched nothing. Expired allows are dropped first; report-only findings never
/// need an allow. The first matching allow takes a finding.
pub fn partition(
    vios: &[Violation],
    allows: &[AllowRule],
    today: Date,
) -> (Vec<Violation>, Vec<AllowRule>) {
    let active: Vec<&AllowRule> = allows
        .iter()
        .filter(|a| !is_expired(&a.expires, today))
        .collect();
    let mut matched = vec![false; active.len()];
    let mut unjustified = Vec::new();
    for v in vios.iter().filter(|v| v.severity == Severity::Violation) {
        match active.iter().position(|a| allow_matches(a, v, today)) {
            Some(i) => matched[i] = true,
            None => unjustified.push(v.clone()),
        }
    }
    let stale = active
        .iter()
        .zip(matched)
        .filter(|(_, m)| !m)
        .map(|(a, _)| (*a).clone())
        .collect();
    (unjustified, stale)
}

#[cfg(test)]
mod tests;
