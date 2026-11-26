//! Go's `url.QueryEscape` and `url.PathEscape`, so request URLs match Go's byte for byte.

fn escape(s: &str, keep: impl Fn(u8) -> bool, space_plus: bool) -> String {
    let mut out = String::with_capacity(s.len());
    for &b in s.as_bytes() {
        if b.is_ascii_alphanumeric() || matches!(b, b'-' | b'_' | b'.' | b'~') || keep(b) {
            out.push(char::from(b));
        } else if b == b' ' && space_plus {
            out.push('+');
        } else {
            out.push_str(&format!("%{b:02X}"));
        }
    }
    out
}

/// `url.QueryEscape`: a space becomes `+`, `/` becomes `%2F`.
pub fn query_escape(s: &str) -> String {
    escape(s, |_| false, true)
}

/// `url.PathEscape`: escapes a single path segment (`/`, `;`, `,`, `?` and the rest).
pub fn path_escape(s: &str) -> String {
    escape(
        s,
        |b| matches!(b, b'$' | b'&' | b'+' | b':' | b'=' | b'@'),
        false,
    )
}

#[cfg(test)]
mod tests;
