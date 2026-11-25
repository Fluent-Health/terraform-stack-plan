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

/// Go's `unicode.IsPrint`, closely enough for the strings this CLI quotes:
/// letters, marks, numbers, punctuation, symbols and the ASCII space.
pub fn is_print(c: char) -> bool {
    if c == ' ' {
        return true;
    }
    if c.is_control() || c.is_whitespace() {
        return false;
    }
    let u = c as u32;
    // Format characters (Cf), private use (Co) and noncharacters are not printable.
    !matches!(u,
        0xAD | 0x600..=0x605 | 0x61C | 0x6DD | 0x70F | 0x180E | 0x200B..=0x200F
        | 0x202A..=0x202E | 0x2060..=0x2064 | 0x2066..=0x206F | 0xFEFF | 0xFFF9..=0xFFFB
        | 0xE000..=0xF8FF | 0xF0000..=0x10FFFF | 0xFFFE | 0xFFFF)
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

/// `fmt.Sprint(f)` / `%v` for a float64: `%g` with the shortest digits, switching to
/// an exponent below 1e-4 or at 1e21 and above.
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
    if !(-4..21).contains(&x) {
        format_e(f)
    } else {
        format_f(f)
    }
}

#[cfg(test)]
mod tests;
