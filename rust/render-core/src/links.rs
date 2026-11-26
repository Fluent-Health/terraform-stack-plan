//! URL templates with `{placeholder}` substitution (`internal/links`).

use std::collections::HashMap;

/// Substitutes `{key}` placeholders from `vars`. A placeholder whose key is missing or empty
/// makes the whole result empty ("no link"). A template without placeholders comes back as is.
pub fn resolve(tmpl: &str, vars: &HashMap<String, String>) -> String {
    let mut out = String::new();
    let mut rest = tmpl;
    loop {
        let Some(open) = rest.find('{') else {
            out.push_str(rest);
            return out;
        };
        let Some(close) = rest[open..].find('}') else {
            out.push_str(rest);
            return out;
        };
        let close = close + open;
        out.push_str(&rest[..open]);
        match vars.get(&rest[open + 1..close]) {
            Some(v) if !v.is_empty() => out.push_str(v),
            _ => return String::new(),
        }
        rest = &rest[close + 1..];
    }
}

#[cfg(test)]
mod tests;
