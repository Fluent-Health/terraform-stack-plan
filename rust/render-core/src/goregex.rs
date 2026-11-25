//! User-supplied regular expressions with Go's `regexp` meaning and error messages.
//!
//! Rust's `regex` follows RE2 like Go does, with one visible difference: `\d`, `\w`, `\s` and
//! `\b` are Unicode-aware in Rust and ASCII-only in Go. Patterns are rewritten to the ASCII
//! forms before compiling. Syntax errors are reported in Go's words
//! (`error parsing regexp: missing closing ]: `[unclosed``).

use regex_syntax::ast::ErrorKind;

/// A compiled pattern.
#[derive(Debug, Clone)]
pub struct GoRegex(regex::Regex);

/// Rewrites Perl classes and word boundaries to their ASCII-only (Go) forms.
fn to_ascii_perl_classes(pattern: &str) -> String {
    let mut out = String::with_capacity(pattern.len());
    let mut chars = pattern.chars().peekable();
    let mut in_class = false;
    let mut class_start = false;
    while let Some(c) = chars.next() {
        if c == '\\' {
            let Some(n) = chars.next() else {
                out.push(c);
                break;
            };
            let rewrite = if in_class {
                match n {
                    'd' => Some("0-9"),
                    'w' => Some("0-9A-Za-z_"),
                    's' => Some("\\t\\n\\f\\r "),
                    'D' => Some("[:^digit:]"),
                    'W' => Some("[:^word:]"),
                    'S' => Some("[:^space:]"),
                    _ => None,
                }
            } else {
                match n {
                    'd' => Some("[0-9]"),
                    'D' => Some("[^0-9]"),
                    'w' => Some("[0-9A-Za-z_]"),
                    'W' => Some("[^0-9A-Za-z_]"),
                    's' => Some("[\\t\\n\\f\\r ]"),
                    'S' => Some("[^\\t\\n\\f\\r ]"),
                    'b' => Some("(?-u:\\b)"),
                    'B' => Some("(?-u:\\B)"),
                    _ => None,
                }
            };
            match rewrite {
                Some(r) => out.push_str(r),
                None => {
                    out.push(c);
                    out.push(n);
                }
            }
            class_start = false;
            continue;
        }
        if in_class {
            if c == '[' && chars.peek() == Some(&':') {
                // A POSIX class like [:alpha:] inside a bracket: copy it through.
                out.push(c);
                for x in chars.by_ref() {
                    out.push(x);
                    if x == ']' {
                        break;
                    }
                }
                class_start = false;
                continue;
            }
            if c == ']' && !class_start {
                in_class = false;
            }
            if !(c == '^' && class_start) {
                class_start = false;
            }
            out.push(c);
            continue;
        }
        if c == '[' {
            in_class = true;
            class_start = true;
        }
        out.push(c);
    }
    out
}

fn go_error(pattern: &str, e: &regex_syntax::ast::Error) -> String {
    let span = e.span();
    let text = pattern.get(span.start.offset..span.end.offset).unwrap_or("");
    let rest = pattern.get(span.start.offset..).unwrap_or("");
    let (code, expr) = match e.kind() {
        ErrorKind::ClassUnclosed => ("missing closing ]", rest),
        ErrorKind::GroupUnclosed => ("missing closing )", pattern),
        ErrorKind::GroupUnopened => ("unexpected )", pattern),
        ErrorKind::RepetitionMissing => ("missing argument to repetition operator", text),
        ErrorKind::EscapeUnexpectedEof => ("trailing backslash at end of expression", ""),
        ErrorKind::EscapeUnrecognized | ErrorKind::EscapeHexInvalid | ErrorKind::EscapeHexInvalidDigit => {
            ("invalid escape sequence", text)
        }
        ErrorKind::ClassRangeInvalid => ("invalid character class range", text),
        ErrorKind::RepetitionCountInvalid
        | ErrorKind::RepetitionCountDecimalEmpty
        | ErrorKind::RepetitionCountUnclosed => ("invalid repeat count", text),
        ErrorKind::GroupNameInvalid | ErrorKind::GroupNameEmpty | ErrorKind::GroupNameDuplicate { .. } => {
            ("invalid named capture", text)
        }
        _ => return format!("error parsing regexp: {}: `{pattern}`", e.kind()),
    };
    format!("error parsing regexp: {code}: `{expr}`")
}

impl GoRegex {
    /// `regexp.Compile`.
    pub fn new(pattern: &str) -> Result<Self, String> {
        regex_syntax::ast::parse::Parser::new()
            .parse(pattern)
            .map_err(|e| go_error(pattern, &e))?;
        regex::Regex::new(&to_ascii_perl_classes(pattern))
            .map(GoRegex)
            .map_err(|e| format!("error parsing regexp: {e}"))
    }

    /// `MatchString`.
    pub fn is_match(&self, s: &str) -> bool {
        self.0.is_match(s)
    }

    /// `NumSubexp`.
    pub fn num_subexp(&self) -> usize {
        self.0.captures_len() - 1
    }

    /// `FindStringSubmatch`: every group's text, `""` for a group that did not take part.
    pub fn find_submatch(&self, s: &str) -> Option<Vec<String>> {
        let caps = self.0.captures(s)?;
        Some(
            (0..caps.len())
                .map(|i| caps.get(i).map_or(String::new(), |m| m.as_str().to_string()))
                .collect(),
        )
    }

    /// `SubexpIndex`: the index of the group with this name, if any.
    pub fn subexp_index(&self, name: &str) -> Option<usize> {
        self.0.capture_names().position(|n| n == Some(name))
    }
}

#[cfg(test)]
mod tests;
