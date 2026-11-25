//! Command-line flags parsed the way Go's `flag` package parses them: `-flag` and `--flag`,
//! `-flag=value` and `-flag value`, booleans that take no value, `--` ending the flags, and
//! parsing stopping at the first non-flag argument.

use std::collections::HashMap;

/// What a flag holds.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Kind {
    /// `fs.String`.
    Str,
    /// `fs.Int` (base-prefixed, underscores allowed, like `strconv.ParseInt(s, 0, 64)`).
    Int,
    /// `fs.Bool`.
    Bool,
    /// A repeatable string (`fs.Var` with an appending value).
    List,
}

/// Why parsing stopped.
#[derive(Debug, PartialEq, Eq)]
pub enum Stop {
    /// `-h`, `-help` or `--help`: Go prints the usage and returns `flag.ErrHelp`.
    Help,
    /// A bad flag: Go prints this message and the usage and returns an error.
    Error(String),
}

/// Parsed values by flag name; a flag that was not given is absent. List flags keep every
/// value, the others only the last.
pub type Values = HashMap<&'static str, Vec<String>>;

/// `strconv.ParseBool`.
pub fn parse_bool(s: &str) -> Option<bool> {
    match s {
        "1" | "t" | "T" | "TRUE" | "true" | "True" => Some(true),
        "0" | "f" | "F" | "FALSE" | "false" | "False" => Some(false),
        _ => None,
    }
}

fn underscore_ok(s: &str) -> bool {
    let s = s.strip_prefix(['+', '-']).unwrap_or(s).as_bytes();
    let mut saw = b'^';
    let mut i = 0;
    let mut hex = false;
    if s.len() >= 2 && s[0] == b'0' && matches!(s[1].to_ascii_lowercase(), b'b' | b'o' | b'x') {
        i = 2;
        saw = b'0';
        hex = s[1].eq_ignore_ascii_case(&b'x');
    }
    while i < s.len() {
        let c = s[i];
        if c.is_ascii_digit() || (hex && c.to_ascii_lowercase().is_ascii_hexdigit()) {
            saw = b'1';
        } else if c == b'_' {
            if saw != b'1' {
                return false;
            }
            saw = b'_';
        } else {
            if saw == b'_' {
                return false;
            }
            saw = b'!';
        }
        i += 1;
    }
    saw != b'_'
}

/// `strconv.ParseInt(s, 0, 64)`; the error is Go's flag wording ("parse error" or "value out of
/// range").
pub fn parse_int(s0: &str) -> Result<i64, &'static str> {
    const SYNTAX: &str = "parse error";
    const RANGE: &str = "value out of range";
    let (neg, s) = match s0.as_bytes().first() {
        Some(b'+') => (false, &s0[1..]),
        Some(b'-') => (true, &s0[1..]),
        _ => (false, s0),
    };
    if s.is_empty() {
        return Err(SYNTAX);
    }
    let b = s.as_bytes();
    let (base, digits) = if b[0] == b'0' {
        if b.len() >= 3 && b[1].eq_ignore_ascii_case(&b'b') {
            (2, &s[2..])
        } else if b.len() >= 3 && b[1].eq_ignore_ascii_case(&b'o') {
            (8, &s[2..])
        } else if b.len() >= 3 && b[1].eq_ignore_ascii_case(&b'x') {
            (16, &s[2..])
        } else {
            (8, &s[1..])
        }
    } else {
        (10, s)
    };
    let mut n: u64 = 0;
    let mut underscores = false;
    for c in digits.chars() {
        if c == '_' {
            underscores = true;
            continue;
        }
        let d = c.to_digit(base).ok_or(SYNTAX)?;
        n = n
            .checked_mul(u64::from(base))
            .and_then(|n| n.checked_add(u64::from(d)))
            .ok_or(RANGE)?;
    }
    if underscores && !underscore_ok(s0) {
        return Err(SYNTAX);
    }
    if neg {
        if n > 1 << 63 {
            return Err(RANGE);
        }
        Ok((n as i64).wrapping_neg())
    } else {
        i64::try_from(n).map_err(|_overflow| RANGE)
    }
}

/// `FlagSet.Parse`. Returns the values and the arguments left after the flags.
pub fn parse(args: &[String], flags: &[(&'static str, Kind)]) -> Result<(Values, Vec<String>), Stop> {
    let mut values = Values::new();
    let mut i = 0;
    while i < args.len() {
        let s = args[i].as_str();
        if s.len() < 2 || !s.starts_with('-') {
            break;
        }
        let mut minuses = 1;
        if s.as_bytes()[1] == b'-' {
            minuses = 2;
            if s.len() == 2 {
                i += 1;
                break;
            }
        }
        let mut name = &s[minuses..];
        if name.is_empty() || name.starts_with('-') || name.starts_with('=') {
            return Err(Stop::Error(format!("bad flag syntax: {s}")));
        }
        i += 1;
        let mut value = None;
        if let Some(eq) = name.bytes().skip(1).position(|c| c == b'=').map(|p| p + 1) {
            value = Some(name[eq + 1..].to_string());
            name = &name[..eq];
        }
        let Some(&(fname, kind)) = flags.iter().find(|(n, _)| *n == name) else {
            if name == "help" || name == "h" {
                return Err(Stop::Help);
            }
            return Err(Stop::Error(format!("flag provided but not defined: -{name}")));
        };
        let value = if kind == Kind::Bool {
            match value {
                Some(v) => match parse_bool(&v) {
                    Some(b) => b.to_string(),
                    None => {
                        return Err(Stop::Error(format!(
                            "invalid boolean value {} for -{name}: parse error",
                            tfstackplan_render::gofmt::quote(&v)
                        )));
                    }
                },
                None => "true".to_string(),
            }
        } else {
            let v = match value {
                Some(v) => v,
                None if i < args.len() => {
                    i += 1;
                    args[i - 1].clone()
                }
                None => return Err(Stop::Error(format!("flag needs an argument: -{name}"))),
            };
            if kind == Kind::Int
                && let Err(e) = parse_int(&v)
            {
                return Err(Stop::Error(format!(
                    "invalid value {} for flag -{name}: {e}",
                    tfstackplan_render::gofmt::quote(&v)
                )));
            }
            v
        };
        let slot = values.entry(fname).or_default();
        if kind != Kind::List {
            slot.clear();
        }
        slot.push(value);
    }
    Ok((values, args[i..].to_vec()))
}

#[cfg(test)]
mod tests;
