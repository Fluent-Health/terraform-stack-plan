//! Go RE2 patterns on the `regex` crate. The syntax is the same; the one
//! difference that bites is that Go's `\d`, `\w`, `\s` and `\b` are ASCII-only
//! while the `regex` crate's are Unicode. [`compile`] rewrites them to their
//! ASCII forms so a pattern matches what it matches in Go.

use regex::Regex;

/// Compiles a Go regexp. The error text is the `regex` crate's, not Go's.
pub fn compile(pattern: &str) -> Result<Regex, regex::Error> {
    Regex::new(&translate(pattern))
}

/// Rewrites Go's ASCII Perl classes for the `regex` crate.
pub fn translate(pattern: &str) -> String {
    let mut out = String::with_capacity(pattern.len() + 8);
    let mut chars = pattern.chars().peekable();
    let mut in_class = false;
    let mut class_start = false;
    while let Some(c) = chars.next() {
        match c {
            '\\' => {
                let Some(n) = chars.next() else {
                    out.push('\\');
                    break;
                };
                let ascii = match (n, in_class) {
                    ('d', false) => Some("(?-u:\\d)"),
                    ('D', false) => Some("(?-u:\\D)"),
                    ('w', false) => Some("(?-u:\\w)"),
                    ('W', false) => Some("(?-u:\\W)"),
                    ('s', false) => Some("[\\t\\n\\x0C\\r ]"),
                    ('S', false) => Some("[^\\t\\n\\x0C\\r ]"),
                    ('b', false) => Some("(?-u:\\b)"),
                    ('B', false) => Some("(?-u:\\B)"),
                    ('d', true) => Some("0-9"),
                    ('w', true) => Some("0-9A-Za-z_"),
                    ('s', true) => Some("\\t\\n\\x0C\\r "),
                    _ => None,
                };
                match ascii {
                    Some(a) => out.push_str(a),
                    None => {
                        out.push('\\');
                        out.push(n);
                    }
                }
                class_start = false;
            }
            '[' if !in_class => {
                in_class = true;
                class_start = true;
                out.push('[');
                if chars.peek() == Some(&'^') {
                    out.push('^');
                    chars.next();
                }
                continue;
            }
            ']' if in_class && !class_start => {
                in_class = false;
                out.push(']');
            }
            c => {
                out.push(c);
                class_start = false;
            }
        }
        if c != '[' {
            class_start = false;
        }
    }
    out
}

#[cfg(test)]
mod tests;
