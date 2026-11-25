//! Number and string formatting that matches Go's `fmt` and `strconv` byte for byte.
//!
//! Go and Rust both print the shortest digits that round-trip a float, so the digits agree;
//! only the layout around them (exponent style, when to switch to exponent form) differs, and
//! that layout is rebuilt here.

use crate::isprint::{IS_NOT_PRINT16, IS_NOT_PRINT32, IS_PRINT16, IS_PRINT32};

/// The shortest round-trip decimal digits of a finite, non-zero float: `0.digits × 10^dp`.
struct Digits {
    neg: bool,
    digits: Vec<u8>,
    dp: i32,
}

fn shortest(v: f64) -> Digits {
    let neg = v.is_sign_negative();
    if v == 0.0 {
        return Digits {
            neg,
            digits: Vec::new(),
            dp: 0,
        };
    }
    // Rust's `{:e}` prints the shortest round-trip digits, e.g. "1.25e-7" or "3e21".
    let s = format!("{:e}", v.abs());
    let (mant, exp) = s.split_once('e').unwrap_or((s.as_str(), "0"));
    let digits: Vec<u8> = mant.bytes().filter(u8::is_ascii_digit).collect();
    let exp: i32 = exp.parse().unwrap_or(0);
    Digits {
        neg,
        digits,
        dp: exp + 1,
    }
}

fn fmt_f(d: &Digits, prec: i32) -> String {
    let mut out = String::new();
    if d.neg {
        out.push('-');
    }
    let nd = d.digits.len() as i32;
    if d.dp > 0 {
        let m = d.dp.min(nd);
        for &c in &d.digits[..m as usize] {
            out.push(c as char);
        }
        for _ in m..d.dp {
            out.push('0');
        }
    } else {
        out.push('0');
    }
    if prec > 0 {
        out.push('.');
        for i in 1..=prec {
            let j = d.dp + i - 1;
            let c = if 0 <= j && j < nd {
                d.digits[j as usize] as char
            } else {
                '0'
            };
            out.push(c);
        }
    }
    out
}

fn fmt_e(d: &Digits, prec: i32) -> String {
    let mut out = String::new();
    if d.neg {
        out.push('-');
    }
    out.push(d.digits.first().map_or('0', |&c| c as char));
    if prec > 0 {
        out.push('.');
        let nd = d.digits.len() as i32;
        let mut i = 1;
        let m = nd.min(prec + 1);
        while i < m {
            out.push(d.digits[i as usize] as char);
            i += 1;
        }
        while i <= prec {
            out.push('0');
            i += 1;
        }
    }
    out.push('e');
    let mut exp = d.dp - 1;
    if d.digits.is_empty() {
        exp = 0;
    }
    if exp < 0 {
        out.push('-');
        exp = -exp;
    } else {
        out.push('+');
    }
    if exp < 10 {
        out.push('0');
    }
    out.push_str(&exp.to_string());
    out
}

fn special(v: f64) -> Option<&'static str> {
    if v.is_nan() {
        Some("NaN")
    } else if v.is_infinite() {
        Some(if v > 0.0 { "+Inf" } else { "-Inf" })
    } else {
        None
    }
}

/// `strconv.FormatFloat(v, 'g', -1, 64)`, which is also what `fmt`'s `%g` and `%v` print.
pub fn format_g(v: f64) -> String {
    if let Some(s) = special(v) {
        return s.to_string();
    }
    let d = shortest(v);
    let nd = d.digits.len() as i32;
    let exp = d.dp - 1;
    if !(-4..6).contains(&exp) {
        fmt_e(&d, nd - 1)
    } else {
        fmt_f(&d, (nd - d.dp).max(0))
    }
}

/// `strconv.FormatFloat(v, 'f', -1, 64)`.
pub fn format_f(v: f64) -> String {
    if let Some(s) = special(v) {
        return s.to_string();
    }
    let d = shortest(v);
    let nd = d.digits.len() as i32;
    fmt_f(&d, (nd - d.dp).max(0))
}

/// How `encoding/json` writes a finite float64.
pub fn format_json_float(v: f64) -> String {
    let abs = v.abs();
    if abs != 0.0 && !(1e-6..1e21).contains(&abs) {
        let d = shortest(v);
        let mut s = fmt_e(&d, d.digits.len() as i32 - 1);
        // Go trims "e-07" to "e-7".
        let b = s.as_bytes();
        let n = b.len();
        if n >= 4 && b[n - 4] == b'e' && b[n - 3] == b'-' && b[n - 2] == b'0' {
            s.remove(n - 2);
        }
        return s;
    }
    format_f(v)
}

/// Go's `int64(f) == f` test for a float that prints as an integer. Go converts an
/// out-of-range float to the minimum int64 on amd64, so only in-range integers qualify.
pub fn as_exact_i64(v: f64) -> Option<i64> {
    if v.is_finite()
        && v.trunc() == v
        && (-9_223_372_036_854_775_808.0..9_223_372_036_854_775_808.0).contains(&v)
    {
        Some(v as i64)
    } else {
        None
    }
}

fn bsearch16(a: &[u16], x: u16) -> usize {
    a.partition_point(|&v| v < x)
}

fn bsearch32(a: &[u32], x: u32) -> usize {
    a.partition_point(|&v| v < x)
}

/// `strconv.IsPrint`.
pub fn is_print(r: char) -> bool {
    let r = r as u32;
    if r <= 0xFF {
        if (0x20..=0x7E).contains(&r) {
            return true;
        }
        if (0xA1..=0xFF).contains(&r) {
            return r != 0xAD;
        }
        return false;
    }
    if r < 0x10000 {
        let rr = r as u16;
        let i = bsearch16(IS_PRINT16, rr);
        if i >= IS_PRINT16.len() || rr < IS_PRINT16[i & !1] || IS_PRINT16[i | 1] < rr {
            return false;
        }
        return IS_NOT_PRINT16.binary_search(&rr).is_err();
    }
    let i = bsearch32(IS_PRINT32, r);
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

/// `strconv.Quote`, which is what `fmt`'s `%q` prints for a string.
pub fn quote(s: &str) -> String {
    let mut out = String::with_capacity(s.len() + 2);
    out.push('"');
    for r in s.chars() {
        if r == '"' || r == '\\' {
            out.push('\\');
            out.push(r);
            continue;
        }
        if is_print(r) {
            out.push(r);
            continue;
        }
        match r {
            '\u{7}' => out.push_str("\\a"),
            '\u{8}' => out.push_str("\\b"),
            '\u{c}' => out.push_str("\\f"),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            '\u{b}' => out.push_str("\\v"),
            _ => {
                let c = r as u32;
                if c < 0x20 || c == 0x7F {
                    out.push_str(&format!("\\x{c:02x}"));
                } else if c < 0x10000 {
                    out.push_str(&format!("\\u{c:04x}"));
                } else {
                    out.push_str(&format!("\\U{c:08x}"));
                }
            }
        }
    }
    out.push('"');
    out
}

#[cfg(test)]
mod tests;
