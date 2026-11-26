use proptest::prelude::*;

use super::*;
use crate::model::{Action, Change, Counts, Field, Leaf, LeafOp, Level, Stack, Variant};

/// A direct port of Go's `Fit`: re-render the whole report after every step.
fn fit_reference(r: &mut Report, budget: i64) -> bool {
    if budget <= 0 {
        return true;
    }
    let size = |r: &Report| render(r).len() as i64;
    while size(r) > budget {
        let mut best: Option<(usize, usize, usize)> = None;
        let mut best_bytes: i64 = -1;
        for (si, s) in r.stacks.iter().enumerate() {
            for (ci, c) in s.changes.iter().enumerate() {
                for (fi, f) in c.fields.iter().enumerate() {
                    if !f.is_block() || f.at_last() {
                        continue;
                    }
                    let bb = f.sel().bytes() as i64;
                    if bb > best_bytes {
                        best = Some((si, ci, fi));
                        best_bytes = bb;
                    }
                }
            }
        }
        let Some((si, ci, fi)) = best else { break };
        r.stacks[si].changes[ci].fields[fi].selected += 1;
    }
    if size(r) <= budget {
        return true;
    }
    let need = size(r);
    r.mode = Mode::SummaryOnly;
    r.notice = "⚠️ Per-stack detail omitted to fit GitHub's size limit (see CI logs / artifact)."
        .to_string();
    if size(r) <= budget {
        return true;
    }
    r.mode = Mode::Minimal;
    r.notice = format!(
        "⚠️ Per-stack table omitted: report needs ~{}, budget {}.",
        human_bytes(need),
        human_bytes(budget)
    );
    size(r) <= budget
}

fn field(name: &str, big: usize) -> Field {
    Field {
        name: name.to_string(),
        variants: vec![
            Variant::new(Level::Structural, "x".repeat(big)),
            Variant::new(Level::Summary, format!("~ {name} summary")),
            Variant::new(Level::Hidden, String::new()),
        ],
        ..Field::default()
    }
}

fn change(address: &str, fields: Vec<Field>) -> Change {
    Change {
        address: address.to_string(),
        type_: "t".to_string(),
        action: Action::Change,
        fields,
        url: String::new(),
        moved: false,
        move_direction: String::new(),
        previous_address: String::new(),
        imported: false,
        import_id: String::new(),
        sensitivity_only: false,
    }
}

fn stack(name: &str, changes: Vec<Change>) -> Stack {
    Stack {
        name: name.to_string(),
        url: String::new(),
        counts: Counts {
            change: 1,
            ..Counts::default()
        },
        categories: Vec::new(),
        changes,
    }
}

fn report_with(fields: Vec<Field>) -> Report {
    Report {
        title: "t".to_string(),
        marker: "m".to_string(),
        stacks: vec![stack("s", vec![change("res.a", fields)])],
        ..Report::default()
    }
}

#[test]
fn no_change_when_under_budget() {
    let mut r = report_with(vec![field("a", 50)]);
    let before = render(&r);
    fit(&mut r, 100_000);
    assert_eq!(render(&r), before);
    assert_eq!(r.stacks[0].changes[0].fields[0].selected, 0);
}

#[test]
fn degrades_largest_first() {
    let mut r = report_with(vec![field("small", 100), field("huge", 5000)]);
    fit(&mut r, 800);
    let f = &r.stacks[0].changes[0].fields;
    assert!(f[1].selected >= f[0].selected);
    assert!(render(&r).len() <= 800);
}

#[test]
fn deterministic() {
    let mut r1 = report_with(vec![field("a", 4000), field("b", 4000)]);
    let mut r2 = report_with(vec![field("a", 4000), field("b", 4000)]);
    fit(&mut r1, 500);
    fit(&mut r2, 500);
    assert_eq!(render(&r1), render(&r2));
}

#[test]
fn terminal_summary_only() {
    let mut r = report_with(vec![field("a", 50)]);
    for i in 0..50 {
        r.stacks.push(stack(
            &format!("stack-{i}"),
            vec![change("r", vec![field("x", 2000)])],
        ));
    }
    fit(&mut r, 300);
    assert_ne!(r.mode, Mode::Full);
    assert!(!r.notice.is_empty());
}

#[test]
fn best_effort_floor_reports_overflow() {
    let mut r = report_with(vec![field("a", 5000)]);
    assert!(!fit(&mut r, 10));
    assert_eq!(r.mode, Mode::Minimal);
}

#[test]
fn fit_skips_leaf_fields() {
    let mut r = report_with(vec![Field {
        name: "x".to_string(),
        leaves: vec![Leaf {
            op: LeafOp::Change,
            path: "x".to_string(),
            old: "1".to_string(),
            new: "2".to_string(),
            inline: String::new(),
        }],
        ..Field::default()
    }]);
    let _ = fit(&mut r, 100_000);
}

#[test]
fn row_cost_matches_blockquote_growth() {
    let r = report_with(vec![field("a", 30), field("b", 3)]);
    let row = render_resource(&r.stacks[0].changes[0], false);
    let mut with = String::new();
    crate::render::stack_section_from_blocks(
        &mut with,
        &r.stacks[0],
        &r,
        &[row.clone(), row.clone()],
    );
    let mut without = String::new();
    crate::render::stack_section_from_blocks(
        &mut without,
        &r.stacks[0],
        &r,
        std::slice::from_ref(&row),
    );
    assert_eq!(with.len() - without.len(), row_cost(&row));
}

fn arb_field() -> impl Strategy<Value = Field> {
    (
        // Mostly a few fixed sizes, so equal-size fields (Go's tie-break) are common.
        prop::collection::vec(
            (
                prop_oneof![
                    3 => prop::sample::select(vec![0usize, 12, 40, 400, 2000]),
                    1 => 0usize..4000,
                ],
                0usize..3,
            ),
            0..4,
        ),
        "[a-z]{1,6}",
        any::<bool>(),
    )
        .prop_map(|(sizes, name, leafy)| {
            if leafy || sizes.is_empty() {
                return Field {
                    name: name.clone(),
                    leaves: vec![Leaf {
                        op: LeafOp::Add,
                        path: name,
                        old: String::new(),
                        new: "\"v\"".to_string(),
                        inline: String::new(),
                    }],
                    ..Field::default()
                };
            }
            let mut variants: Vec<Variant> = sizes
                .iter()
                .map(|&(n, lines)| {
                    let line = "y".repeat(n / (lines + 1));
                    Variant::new(Level::Structural, vec![line; lines + 1].join("\n"))
                })
                .collect();
            variants.push(Variant::new(Level::Hidden, String::new()));
            Field {
                name,
                kind: "yaml".to_string(),
                variants,
                ..Field::default()
            }
        })
}

fn arb_report() -> impl Strategy<Value = Report> {
    let arb_change =
        prop::collection::vec(arb_field(), 0..4).prop_map(|fields| change("res.x", fields));
    let arb_stack = (
        prop::collection::vec(arb_change, 0..4),
        any::<bool>(),
        0u8..3,
    )
        .prop_map(|(changes, has_counts, extra)| {
            let mut s = stack("s", changes);
            if !has_counts {
                s.counts = Counts::default();
            }
            s.counts.moved = i64::from(extra);
            s
        });
    (
        prop::collection::vec(arb_stack, 0..5),
        any::<bool>(),
        any::<bool>(),
    )
        .prop_map(|(mut stacks, open, classified)| {
            for (i, s) in stacks.iter_mut().enumerate() {
                s.name = format!("stack-{i}");
            }
            Report {
                title: "t".to_string(),
                marker: "m".to_string(),
                classified,
                stacks,
                details_open: open,
                ..Report::default()
            }
        })
}

proptest! {
    #![proptest_config(ProptestConfig::with_cases(400))]

    #[test]
    fn incremental_fit_matches_go_loop(report in arb_report(), budget in -10i64..30_000) {
        let mut fast = report.clone();
        let mut slow = report;
        let a = fit(&mut fast, budget);
        let b = fit_reference(&mut slow, budget);
        prop_assert_eq!(a, b);
        prop_assert_eq!(&fast, &slow);
        prop_assert_eq!(render(&fast), render(&slow));
    }
}
