//! Go's `filepath.Match` on unix, ported line for line: `*` and `?` stop at `/`,
//! `[...]` classes with ranges and `^` negation, `\` escapes. Ported from Go's
//! `path/filepath/match.go`, Copyright 2010 The Go Authors, BSD-3-Clause; the licence text is in
//! `rust/THIRD_PARTY_NOTICES`.

/// The pattern is malformed (Go's `filepath.ErrBadPattern`).
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct BadPattern;

impl std::fmt::Display for BadPattern {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str("syntax error in pattern")
    }
}

/// `filepath.Match(pattern, name)`.
pub fn matches(mut pattern: &str, mut name: &str) -> Result<bool, BadPattern> {
    'pattern: while !pattern.is_empty() {
        let (star, chunk, rest) = scan_chunk(pattern);
        pattern = rest;
        if star && chunk.is_empty() {
            return Ok(!name.contains('/'));
        }
        let (t, ok, err) = match_chunk(chunk, name);
        if ok && (t.is_empty() || !pattern.is_empty()) {
            name = t;
            continue;
        }
        if let Some(e) = err {
            return Err(e);
        }
        if star {
            for (i, c) in name.char_indices() {
                if c == '/' {
                    break;
                }
                let _ = i;
                let skip = &name[i + c.len_utf8()..];
                // Go skips byte by byte; skipping by whole characters only differs
                // inside a multi-byte character, where no chunk can match anyway.
                let (t, ok, err) = match_chunk(chunk, skip);
                if ok {
                    if pattern.is_empty() && !t.is_empty() {
                        continue;
                    }
                    name = t;
                    continue 'pattern;
                }
                if let Some(e) = err {
                    return Err(e);
                }
            }
        }
        while !pattern.is_empty() {
            let (_, chunk, rest) = scan_chunk(pattern);
            pattern = rest;
            if let (_, _, Some(e)) = match_chunk(chunk, "") {
                return Err(e);
            }
        }
        return Ok(false);
    }
    Ok(name.is_empty())
}

fn scan_chunk(mut pattern: &str) -> (bool, &str, &str) {
    let mut star = false;
    while let Some(rest) = pattern.strip_prefix('*') {
        pattern = rest;
        star = true;
    }
    let b = pattern.as_bytes();
    let mut inrange = false;
    let mut i = 0;
    while i < b.len() {
        match b[i] {
            b'\\' => {
                if i + 1 < b.len() {
                    i += 1;
                }
            }
            b'[' => inrange = true,
            b']' => inrange = false,
            b'*' if !inrange => break,
            _ => {}
        }
        i += 1;
    }
    (star, &pattern[..i], &pattern[i..])
}

fn first_char(s: &str) -> Option<char> {
    s.chars().next()
}

fn match_chunk<'a>(mut chunk: &str, mut s: &'a str) -> (&'a str, bool, Option<BadPattern>) {
    let mut failed = false;
    while !chunk.is_empty() {
        if !failed && s.is_empty() {
            failed = true;
        }
        let c0 = chunk.as_bytes()[0];
        match c0 {
            b'[' => {
                let mut r = '\u{0}';
                if !failed && let Some(c) = first_char(s) {
                    r = c;
                    s = &s[c.len_utf8()..];
                }
                chunk = &chunk[1..];
                let mut negated = false;
                if let Some(rest) = chunk.strip_prefix('^') {
                    negated = true;
                    chunk = rest;
                }
                let mut matched = false;
                let mut nrange = 0;
                loop {
                    if nrange > 0
                        && let Some(rest) = chunk.strip_prefix(']')
                    {
                        chunk = rest;
                        break;
                    }
                    let lo;
                    match get_esc(chunk) {
                        Ok((c, rest)) => {
                            lo = c;
                            chunk = rest;
                        }
                        Err(e) => return ("", false, Some(e)),
                    }
                    let mut hi = lo;
                    if let Some(rest) = chunk.strip_prefix('-') {
                        match get_esc(rest) {
                            Ok((c, rest)) => {
                                hi = c;
                                chunk = rest;
                            }
                            Err(e) => return ("", false, Some(e)),
                        }
                    }
                    if lo <= r && r <= hi {
                        matched = true;
                    }
                    nrange += 1;
                }
                if matched == negated {
                    failed = true;
                }
            }
            b'?' => {
                if !failed && let Some(c) = first_char(s) {
                    if c == '/' {
                        failed = true;
                    }
                    s = &s[c.len_utf8()..];
                }
                chunk = &chunk[1..];
            }
            _ => {
                if c0 == b'\\' {
                    chunk = &chunk[1..];
                    if chunk.is_empty() {
                        return ("", false, Some(BadPattern));
                    }
                }
                let pc = first_char(chunk).unwrap_or('\u{0}');
                if !failed {
                    if first_char(s) != Some(pc) {
                        failed = true;
                    }
                    if let Some(c) = first_char(s) {
                        s = &s[c.len_utf8()..];
                    }
                }
                chunk = &chunk[pc.len_utf8()..];
            }
        }
    }
    if failed {
        return ("", false, None);
    }
    (s, true, None)
}

fn get_esc(mut chunk: &str) -> Result<(char, &str), BadPattern> {
    if chunk.is_empty() || chunk.starts_with('-') || chunk.starts_with(']') {
        return Err(BadPattern);
    }
    if let Some(rest) = chunk.strip_prefix('\\') {
        chunk = rest;
        if chunk.is_empty() {
            return Err(BadPattern);
        }
    }
    let c = first_char(chunk).ok_or(BadPattern)?;
    let rest = &chunk[c.len_utf8()..];
    if rest.is_empty() {
        return Err(BadPattern);
    }
    Ok((c, rest))
}

#[cfg(test)]
mod tests;
