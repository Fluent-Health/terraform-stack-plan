//! Go's text formatting where the CLI output depends on it: `strconv.Quote`
//! (what `%q` prints) and the `%v` form of a float64.

/// `strconv.Quote(s)`: a double-quoted Go string literal.
pub fn quote(s: &str) -> String {
    let mut out = String::with_capacity(s.len() + 2);
    out.push('"');
    for c in s.chars() {
        match c {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            c if is_print(c) => out.push(c),
            '\u{7}' => out.push_str("\\a"),
            '\u{8}' => out.push_str("\\b"),
            '\u{c}' => out.push_str("\\f"),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            '\u{b}' => out.push_str("\\v"),
            c if (c as u32) < 0x20 || c as u32 == 0x7f => {
                out.push_str(&format!("\\x{:02x}", c as u32));
            }
            c if (c as u32) < 0x10000 => out.push_str(&format!("\\u{:04x}", c as u32)),
            c => out.push_str(&format!("\\U{:08x}", c as u32)),
        }
    }
    out.push('"');
    out
}

/// `strconv.IsPrint`, from Go's own tables (see `isprint.rs`).
pub fn is_print(c: char) -> bool {
    use crate::isprint::{IS_NOT_PRINT16, IS_NOT_PRINT32, IS_PRINT16, IS_PRINT32};
    let r = c as u32;
    if r <= 0xFF {
        return (0x20..=0x7E).contains(&r) || ((0xA1..=0xFF).contains(&r) && r != 0xAD);
    }
    if r < 0x10000 {
        let rr = r as u16;
        let i = IS_PRINT16.partition_point(|&v| v < rr);
        if i >= IS_PRINT16.len() || rr < IS_PRINT16[i & !1] || IS_PRINT16[i | 1] < rr {
            return false;
        }
        return IS_NOT_PRINT16.binary_search(&rr).is_err();
    }
    let i = IS_PRINT32.partition_point(|&v| v < r);
    if i >= IS_PRINT32.len() || r < IS_PRINT32[i & !1] || IS_PRINT32[i | 1] < r {
        return false;
    }
    if r >= 0x20000 {
        return true;
    }
    IS_NOT_PRINT32
        .binary_search(&((r - 0x10000) as u16))
        .is_err()
}

/// The shortest decimal digits and exponent of `f` (finite, non-zero):
/// `f = 0.d1d2d3… × 10^exp`, as Rust's shortest round-trip formatting gives them.
fn shortest(f: f64) -> (String, i32) {
    // `{:e}` is shortest round-trip: "1.2345e-7", "5e0".
    let s = format!("{:e}", f.abs());
    let (mant, exp) = s.split_once('e').unwrap_or((&s, "0"));
    let digits: String = mant.chars().filter(|c| *c != '.').collect();
    let exp: i32 = exp.parse().unwrap_or(0);
    (digits, exp + 1)
}

/// `strconv.FormatFloat(f, 'e', -1, 64)`: "1.5e+21", "1e-07".
pub fn format_e(f: f64) -> String {
    let (digits, exp) = shortest(f);
    let sign = if f.is_sign_negative() { "-" } else { "" };
    let mut mant = digits[..1].to_string();
    if digits.len() > 1 {
        mant.push('.');
        mant.push_str(&digits[1..]);
    }
    let e = exp - 1;
    let esign = if e < 0 { '-' } else { '+' };
    format!("{sign}{mant}e{esign}{:02}", e.abs())
}

/// `strconv.FormatFloat(f, 'f', -1, 64)`: shortest digits, never an exponent.
pub fn format_f(f: f64) -> String {
    // Rust's Display for f64 is exactly this.
    format!("{f}")
}

/// `fmt.Sprint(f)` / `%v` for a float64: strconv's `'g'` with the shortest digits. For the
/// shortest form strconv switches to an exponent when it is below -4 or at least 6
/// (`ftoa.go`: `if shortest { eprec = 6 }`), so 1e6 prints as `1e+06`.
pub fn format_v(f: f64) -> String {
    if f.is_nan() {
        return "NaN".to_string();
    }
    if f.is_infinite() {
        return if f > 0.0 { "+Inf" } else { "-Inf" }.to_string();
    }
    if f == 0.0 {
        return if f.is_sign_negative() { "-0" } else { "0" }.to_string();
    }
    let (_, exp) = shortest(f);
    let x = exp - 1;
    if !(-4..6).contains(&x) {
        format_e(f)
    } else {
        format_f(f)
    }
}

#[cfg(test)]
mod tests;
