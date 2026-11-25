//! A port of the parts of `github.com/pmezard/go-difflib` that `tfstackplan` uses: Python's
//! `difflib.SequenceMatcher` (with its auto-junk heuristic) and the unified diff writer, without
//! file headers.

use std::collections::{HashMap, HashSet};

/// `difflib.SplitLines`: split after every newline and add a newline to the last piece, so
/// `"a\n"` becomes `["a\n", "\n"]`.
pub fn split_lines(s: &str) -> Vec<String> {
    let mut lines: Vec<String> = s.split_inclusive('\n').map(str::to_string).collect();
    if s.is_empty() || s.ends_with('\n') {
        lines.push(String::new());
    }
    if let Some(last) = lines.last_mut() {
        last.push('\n');
    }
    lines
}

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
struct Match {
    a: usize,
    b: usize,
    size: usize,
}

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
struct OpCode {
    tag: u8,
    i1: usize,
    i2: usize,
    j1: usize,
    j2: usize,
}

struct Matcher<'a> {
    a: &'a [String],
    b: &'a [String],
    b2j: HashMap<&'a str, Vec<usize>>,
}

impl<'a> Matcher<'a> {
    fn new(a: &'a [String], b: &'a [String]) -> Self {
        let mut b2j: HashMap<&'a str, Vec<usize>> = HashMap::new();
        for (i, s) in b.iter().enumerate() {
            b2j.entry(s.as_str()).or_default().push(i);
        }
        // No junk function. Auto-junk: purge popular elements of a long b.
        let n = b.len();
        if n >= 200 {
            let ntest = n / 100 + 1;
            let popular: HashSet<&str> = b2j
                .iter()
                .filter(|(_, v)| v.len() > ntest)
                .map(|(k, _)| *k)
                .collect();
            for s in popular {
                b2j.remove(s);
            }
        }
        Matcher { a, b, b2j }
    }

    fn find_longest_match(&self, alo: usize, ahi: usize, blo: usize, bhi: usize) -> Match {
        let (mut besti, mut bestj, mut bestsize) = (alo, blo, 0usize);
        let mut j2len: HashMap<usize, usize> = HashMap::new();
        for i in alo..ahi {
            let mut newj2len: HashMap<usize, usize> = HashMap::new();
            if let Some(js) = self.b2j.get(self.a[i].as_str()) {
                for &j in js {
                    if j < blo {
                        continue;
                    }
                    if j >= bhi {
                        break;
                    }
                    let k = j.checked_sub(1).and_then(|p| j2len.get(&p)).copied().unwrap_or(0) + 1;
                    newj2len.insert(j, k);
                    if k > bestsize {
                        besti = i + 1 - k;
                        bestj = j + 1 - k;
                        bestsize = k;
                    }
                }
            }
            j2len = newj2len;
        }
        // There is no junk, so the junk-only extension loops of the original never run.
        while besti > alo && bestj > blo && self.a[besti - 1] == self.b[bestj - 1] {
            besti -= 1;
            bestj -= 1;
            bestsize += 1;
        }
        while besti + bestsize < ahi
            && bestj + bestsize < bhi
            && self.a[besti + bestsize] == self.b[bestj + bestsize]
        {
            bestsize += 1;
        }
        Match {
            a: besti,
            b: bestj,
            size: bestsize,
        }
    }

    fn matching_blocks(&self) -> Vec<Match> {
        let mut matched = Vec::new();
        // The original recurses left, appends the match, then recurses right. A work list of
        // ranges to search and matches to emit keeps that order without recursion.
        enum Work {
            Range(usize, usize, usize, usize),
            Emit(Match),
        }
        let mut work = vec![Work::Range(0, self.a.len(), 0, self.b.len())];
        while let Some(w) = work.pop() {
            match w {
                Work::Emit(m) => matched.push(m),
                Work::Range(alo, ahi, blo, bhi) => {
                    let m = self.find_longest_match(alo, ahi, blo, bhi);
                    if m.size > 0 {
                        let (i, j, k) = (m.a, m.b, m.size);
                        if i + k < ahi && j + k < bhi {
                            work.push(Work::Range(i + k, ahi, j + k, bhi));
                        }
                        work.push(Work::Emit(m));
                        if alo < i && blo < j {
                            work.push(Work::Range(alo, i, blo, j));
                        }
                    }
                }
            }
        }

        let mut non_adjacent = Vec::new();
        let (mut i1, mut j1, mut k1) = (0, 0, 0);
        for m in matched {
            if i1 + k1 == m.a && j1 + k1 == m.b {
                k1 += m.size;
            } else {
                if k1 > 0 {
                    non_adjacent.push(Match {
                        a: i1,
                        b: j1,
                        size: k1,
                    });
                }
                (i1, j1, k1) = (m.a, m.b, m.size);
            }
        }
        if k1 > 0 {
            non_adjacent.push(Match {
                a: i1,
                b: j1,
                size: k1,
            });
        }
        non_adjacent.push(Match {
            a: self.a.len(),
            b: self.b.len(),
            size: 0,
        });
        non_adjacent
    }

    fn op_codes(&self) -> Vec<OpCode> {
        let (mut i, mut j) = (0, 0);
        let mut codes = Vec::new();
        for m in self.matching_blocks() {
            let tag = if i < m.a && j < m.b {
                b'r'
            } else if i < m.a {
                b'd'
            } else if j < m.b {
                b'i'
            } else {
                0
            };
            if tag > 0 {
                codes.push(OpCode {
                    tag,
                    i1: i,
                    i2: m.a,
                    j1: j,
                    j2: m.b,
                });
            }
            i = m.a + m.size;
            j = m.b + m.size;
            if m.size > 0 {
                codes.push(OpCode {
                    tag: b'e',
                    i1: m.a,
                    i2: i,
                    j1: m.b,
                    j2: j,
                });
            }
        }
        codes
    }

    fn grouped_op_codes(&self, n: usize) -> Vec<Vec<OpCode>> {
        let mut codes = self.op_codes();
        if codes.is_empty() {
            codes.push(OpCode {
                tag: b'e',
                i1: 0,
                i2: 1,
                j1: 0,
                j2: 1,
            });
        }
        if let Some(c) = codes.first_mut()
            && c.tag == b'e'
        {
            c.i1 = c.i1.max(c.i2.saturating_sub(n));
            c.j1 = c.j1.max(c.j2.saturating_sub(n));
        }
        if let Some(c) = codes.last_mut()
            && c.tag == b'e'
        {
            c.i2 = c.i2.min(c.i1 + n);
            c.j2 = c.j2.min(c.j1 + n);
        }
        let nn = n + n;
        let mut groups = Vec::new();
        let mut group = Vec::new();
        for c in codes {
            let (mut i1, i2, mut j1, j2) = (c.i1, c.i2, c.j1, c.j2);
            if c.tag == b'e' && i2 - i1 > nn {
                group.push(OpCode {
                    tag: c.tag,
                    i1,
                    i2: i2.min(i1 + n),
                    j1,
                    j2: j2.min(j1 + n),
                });
                groups.push(std::mem::take(&mut group));
                i1 = i1.max(i2.saturating_sub(n));
                j1 = j1.max(j2.saturating_sub(n));
            }
            group.push(OpCode {
                tag: c.tag,
                i1,
                i2,
                j1,
                j2,
            });
        }
        if !group.is_empty() && !(group.len() == 1 && group[0].tag == b'e') {
            groups.push(group);
        }
        groups
    }
}

fn format_range(start: usize, stop: usize) -> String {
    let mut beginning = start + 1;
    let length = stop - start;
    if length == 1 {
        return beginning.to_string();
    }
    if length == 0 {
        beginning -= 1;
    }
    format!("{beginning},{length}")
}

/// `difflib.GetUnifiedDiffString` with no file names: hunks only, each starting with an `@@`
/// line, every line ending in a newline.
pub fn unified_diff(a: &[String], b: &[String], context: usize) -> String {
    let m = Matcher::new(a, b);
    let mut out = String::new();
    for g in m.grouped_op_codes(context) {
        let (first, last) = (g[0], g[g.len() - 1]);
        out.push_str(&format!(
            "@@ -{} +{} @@\n",
            format_range(first.i1, last.i2),
            format_range(first.j1, last.j2)
        ));
        for c in &g {
            if c.tag == b'e' {
                for line in &a[c.i1..c.i2] {
                    out.push(' ');
                    out.push_str(line);
                }
                continue;
            }
            if c.tag == b'r' || c.tag == b'd' {
                for line in &a[c.i1..c.i2] {
                    out.push('-');
                    out.push_str(line);
                }
            }
            if c.tag == b'r' || c.tag == b'i' {
                for line in &b[c.j1..c.j2] {
                    out.push('+');
                    out.push_str(line);
                }
            }
        }
    }
    out
}

#[cfg(test)]
mod tests;
