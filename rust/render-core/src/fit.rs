//! Reduces a report until its rendered size fits a byte budget (`internal/fit`): degrade the
//! largest attribute diff first, then fall back through a report-level cascade.
//!
//! Go re-renders the whole report after every single degradation step, which is quadratic. This
//! port makes the same choices in the same order but keeps the size up to date incrementally: a
//! step only changes one resource row, and a row's share of the document is a pure function of
//! its own rendered text (see [`row_cost`]). The tests check it against a direct port of the Go
//! loop.

use std::cmp::Reverse;
use std::collections::BinaryHeap;

use crate::model::{Mode, Report};
use crate::render::{render, render_resource};

/// Bytes one resource row adds to its stack section. The section body is the rows joined by
/// blank lines after a leading blank line, then blockquoted: every line gets "> " (or ">" when
/// empty) and a newline. So a row costs its own lines with prefixes and newlines, plus 2 for the
/// blank line in front of it.
pub fn row_cost(row: &str) -> usize {
    2 + row
        .split('\n')
        .map(|l| l.len() + if l.is_empty() { 1 } else { 2 } + 1)
        .sum::<usize>()
}

/// Mutates `r` to fit `budget` bytes; `budget <= 0` disables reduction. Returns whether the
/// rendered report fits.
pub fn fit(r: &mut Report, budget: i64) -> bool {
    if budget <= 0 {
        return true;
    }
    let budget_bytes = usize::try_from(budget).unwrap_or(usize::MAX);
    let mut size = render(r).len();

    // Phase 1: per-attribute, largest first; ties keep the earliest (stack, change, field).
    if size > budget_bytes {
        let mut costs: Vec<Vec<usize>> = r
            .stacks
            .iter()
            .map(|s| s.changes.iter().map(|c| row_cost(&render_resource(c, r.details_open))).collect())
            .collect();
        let mut heap = BinaryHeap::new();
        for (si, s) in r.stacks.iter().enumerate() {
            for (ci, c) in s.changes.iter().enumerate() {
                for (fi, f) in c.fields.iter().enumerate() {
                    if f.is_block() && !f.at_last() {
                        heap.push((f.sel().bytes(), Reverse((si, ci, fi))));
                    }
                }
            }
        }
        while size > budget_bytes {
            let Some((_, Reverse((si, ci, fi)))) = heap.pop() else {
                break;
            };
            let details_open = r.details_open;
            let stack = &mut r.stacks[si];
            let shown = stack.counts.any_change();
            let change = &mut stack.changes[ci];
            let field = &mut change.fields[fi];
            field.selected += 1;
            if !field.at_last() {
                heap.push((field.sel().bytes(), Reverse((si, ci, fi))));
            }
            if shown {
                let cost = row_cost(&render_resource(change, details_open));
                size = size + cost - costs[si][ci];
                costs[si][ci] = cost;
            }
        }
    }
    if size <= budget_bytes {
        return true;
    }

    // Phase 2: terminal cascade. The "needs" figure is the size before any mode change.
    let need = size;
    r.mode = Mode::SummaryOnly;
    r.notice = "⚠️ Per-stack detail omitted to fit GitHub's size limit (see CI logs / artifact).".to_string();
    if render(r).len() <= budget_bytes {
        return true;
    }
    r.mode = Mode::Minimal;
    r.notice = format!(
        "⚠️ Per-stack table omitted: report needs ~{}, budget {}.",
        human_bytes(need as i64),
        human_bytes(budget)
    );
    render(r).len() <= budget_bytes
}

/// "N B" under 1 KB, else "N KB".
pub fn human_bytes(n: i64) -> String {
    if n < 1024 {
        format!("{n} B")
    } else {
        format!("{} KB", n / 1024)
    }
}

#[cfg(test)]
mod tests;
