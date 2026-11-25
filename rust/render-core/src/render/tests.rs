use super::*;
use crate::model::{Category, Field, Leaf, LeafOp, Level, Link, Variant};

const META: &str = META_INDENT;

fn cat(name: &str, icon: &str) -> Category {
    Category {
        name: name.to_string(),
        icon: icon.to_string(),
        attributes: None,
    }
}

fn leaf(name: &str, op: LeafOp, path: &str, old: &str, new: &str) -> Field {
    Field {
        name: name.to_string(),
        leaves: vec![Leaf {
            op,
            path: path.to_string(),
            old: old.to_string(),
            new: new.to_string(),
            inline: String::new(),
        }],
        ..Field::default()
    }
}

fn block(name: &str, variants: Vec<Variant>) -> Field {
    Field {
        name: name.to_string(),
        variants,
        ..Field::default()
    }
}

fn change(address: &str, type_: &str, action: Action, fields: Vec<Field>) -> Change {
    Change {
        address: address.to_string(),
        type_: type_.to_string(),
        action,
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

fn stack(name: &str, counts: Counts) -> Stack {
    Stack {
        name: name.to_string(),
        url: String::new(),
        counts,
        categories: Vec::new(),
        changes: Vec::new(),
    }
}

fn sample_report() -> Report {
    let mut s0 = stack(
        "platform/nonprod",
        Counts {
            change: 1,
            ..Counts::default()
        },
    );
    s0.categories = vec![cat("iam", "🔐"), cat("destructive", "💣")];
    s0.changes = vec![change(
        "google_storage_bucket.tfstate",
        "google_storage_bucket",
        Action::Change,
        vec![
            leaf("labels", LeafOp::Add, "labels.team", "", "\"platform\""),
            leaf(
                "retention_days",
                LeafOp::Change,
                "retention_days",
                "7",
                "30",
            ),
        ],
    )];
    Report {
        title: "Terraform plan — nonprod".to_string(),
        marker: "tfstackplan:nonprod".to_string(),
        classified: true,
        default: cat("safe", "✅"),
        stacks: vec![
            s0,
            stack(
                "svc/dev",
                Counts {
                    add: 2,
                    ..Counts::default()
                },
            ),
        ],
        ..Report::default()
    }
}

fn counts(f: impl FnOnce(&mut Counts)) -> Counts {
    let mut c = Counts::default();
    f(&mut c);
    c
}

#[test]
fn render_marker_first() {
    assert!(render(&sample_report()).starts_with("<!-- tfstackplan:nonprod -->\n"));
}

#[test]
fn render_zero_column_omitted() {
    let out = render(&sample_report());
    assert!(
        !out.contains("Destroy") && !out.contains("Replace"),
        "{out}"
    );
    assert!(out.contains("Add") && out.contains("Change"), "{out}");
}

#[test]
fn render_categories_column_and_details() {
    let out = render(&sample_report());
    assert!(out.contains("Categories"));
    assert!(out.contains("🔐 iam") && out.contains("💣 destructive"));
    assert!(out.contains("🔐 iam  💣 destructive"));
    assert!(out.contains("<b>svc/dev</b> · ✅ safe"));
    assert!(out.contains("✅ safe"));
    assert!(out.contains("<details><summary>📁&nbsp;<b>platform/nonprod</b>"));
    assert!(out.contains("```diff"));
}

#[test]
fn render_no_categories_column_when_unclassified() {
    let mut r = sample_report();
    r.classified = false;
    for s in &mut r.stacks {
        s.categories.clear();
    }
    assert!(!render(&r).contains("Categories"));
}

#[test]
fn render_details_open() {
    let mut r = sample_report();
    r.details_open = true;
    assert!(render(&r).contains("<details open><summary>"));
}

#[test]
fn render_summary_only_mode() {
    let mut r = sample_report();
    r.mode = Mode::SummaryOnly;
    r.notice = "⚠️ Per-stack detail omitted to fit GitHub's size limit (see CI logs / artifact)."
        .to_string();
    let out = render(&r);
    assert!(!out.contains("<details>"), "{out}");
    assert!(out.contains(&r.notice));
}

#[test]
fn render_minimal_mode() {
    let mut r = sample_report();
    r.mode = Mode::Minimal;
    r.notice = "⚠️ Per-stack table omitted: report needs ~120 KB, budget 60 KB.".to_string();
    let out = render(&r);
    assert!(!out.contains("| Stack"), "{out}");
    assert!(out.contains("2 stacks"), "{out}");
}

#[test]
fn render_update_leaves_aligned() {
    let out = render(&sample_report());
    assert!(out.contains("+ labels.team    = \"platform\""), "{out}");
    assert!(out.contains("~ retention_days = 7 → 30"), "{out}");
}

#[test]
fn render_create_small_is_open_row() {
    let mut r = sample_report();
    r.stacks[0].changes = vec![change(
        "google_service_account.api",
        "google_service_account",
        Action::Add,
        vec![
            leaf("account_id", LeafOp::Add, "account_id", "", "\"app-api\""),
            leaf("disabled", LeafOp::Add, "disabled", "", "false"),
        ],
    )];
    r.stacks[0].counts = counts(|c| c.add = 1);
    let out = render(&r);
    assert!(
        out.contains(&format!(
            "<details open><summary>➕&nbsp;google_service_account.api<br>{META}2 attrs</summary>"
        )),
        "{out}"
    );
    assert!(out.contains("+ account_id = \"app-api\""), "{out}");
}

#[test]
fn render_resource_closed_when_big() {
    let mut big = String::new();
    for i in 0..15 {
        big.push_str(&format!("- line {i}\n+ line {i}\n"));
    }
    let mut r = sample_report();
    r.stacks[0].changes = vec![change(
        "kubernetes_config_map.app",
        "kubernetes_config_map",
        Action::Change,
        vec![block("data", vec![Variant::new(Level::LineDiff, big)])],
    )];
    r.stacks[0].counts = counts(|c| c.change = 1);
    let out = render(&r);
    assert!(
        out.contains(&format!(
            "<details><summary>✏️&nbsp;kubernetes_config_map.app<br>{META}1 changed</summary>"
        )),
        "{out}"
    );
    assert!(!out.contains("<details open><summary>✏️&nbsp;kubernetes_config_map.app"));
    assert!(out.contains("+ line 0"));
}

#[test]
fn render_blockquote_bar() {
    assert!(render(&sample_report()).contains("> <details"));
}

#[test]
fn render_stack_header_and_spacing() {
    let mut r = sample_report();
    r.stacks[0].counts = counts(|c| c.add = 2);
    r.stacks[0].changes = vec![
        change(
            "x.a",
            "",
            Action::Add,
            vec![leaf("n", LeafOp::Add, "n", "", "\"1\"")],
        ),
        change(
            "x.b",
            "",
            Action::Add,
            vec![leaf("n", LeafOp::Add, "n", "", "\"2\"")],
        ),
    ];
    let out = render(&r);
    assert!(
        out.contains("<summary>📁&nbsp;<b>platform/nonprod</b>"),
        "{out}"
    );
    assert!(out.contains("</summary>\n\n>\n> <details"), "{out}");
    assert!(out.contains("</details>\n>\n> <details"), "{out}");
}

#[test]
fn render_forget_row() {
    let mut r = sample_report();
    r.stacks[0].counts = counts(|c| c.forget = 1);
    r.stacks[0].changes = vec![change(
        "aws_s3_bucket.legacy",
        "",
        Action::Forget,
        vec![
            leaf("bucket", LeafOp::Remove, "bucket", "\"legacy\"", ""),
            leaf("region", LeafOp::Remove, "region", "\"us-east-1\"", ""),
        ],
    )];
    let out = render(&r);
    assert!(
        out.contains(&format!(
            "⏏️&nbsp;aws_s3_bucket.legacy<br>{META}forgotten · 2 attrs"
        )),
        "{out}"
    );
    assert!(out.contains("⊘ bucket = \"legacy\""), "{out}");
}

#[test]
fn render_moved_row() {
    let mut r = sample_report();
    r.stacks[0].counts = counts(|c| c.moved = 1);
    let mut c = change("google_storage_bucket.assets", "", Action::Noop, vec![]);
    c.moved = true;
    c.previous_address = "google_storage_bucket.legacy_assets".to_string();
    r.stacks[0].changes = vec![c];
    let out = render(&r);
    assert!(
        out.contains(&format!(
            "↪️&nbsp;google_storage_bucket.assets<br>{META}moved from google_storage_bucket.legacy_assets"
        )),
        "{out}"
    );
    assert!(out.contains("(address change only)"));
}

#[test]
fn render_directional_moved_rows() {
    let row = |dir: &str, fields: Vec<Field>| {
        let mut r = sample_report();
        r.stacks[0].counts = counts(|c| c.moved = 1);
        let mut c = change("google_storage_bucket.assets", "", Action::Noop, fields);
        c.moved = true;
        c.move_direction = dir.to_string();
        r.stacks[0].changes = vec![c];
        render(&r)
    };
    let out1 = row("out", vec![]);
    assert!(out1.contains(&format!(
        "↗️&nbsp;google_storage_bucket.assets<br>{META}moved out (relocating)"
    )));
    let out2 = row("in", vec![]);
    assert!(out2.contains(&format!(
        "↙️&nbsp;google_storage_bucket.assets<br>{META}moved in (cross-state)"
    )));
    let out3 = row(
        "in-update",
        vec![leaf(
            "labels",
            LeafOp::Add,
            "labels.team",
            "",
            "\"platform\"",
        )],
    );
    assert!(out3.contains(&format!(
        "🚚✏️&nbsp;google_storage_bucket.assets<br>{META}moved in and updated"
    )));
    assert!(out3.contains("+ labels.team = \"platform\""));
}

#[test]
fn render_imported_row() {
    let mut r = sample_report();
    r.stacks[0].counts = counts(|c| c.import = 1);
    let mut c = change("google_project.host", "", Action::Noop, vec![]);
    c.imported = true;
    c.import_id = "my-host-project".to_string();
    r.stacks[0].changes = vec![c];
    let out = render(&r);
    assert!(
        out.contains(&format!(
            "📥&nbsp;google_project.host<br>{META}imported · id=<code>my-host-project</code>"
        )),
        "{out}"
    );
}

#[test]
fn render_table_extras_suffix() {
    let mut r = sample_report();
    r.stacks[0].counts = counts(|c| {
        c.moved = 1;
        c.import = 1;
        c.forget = 1;
    });
    assert!(render(&r).contains("platform/nonprod · 1 import, 1 move, 1 forget |"));
}

#[test]
fn render_create_block_field_rendered() {
    let mut r = sample_report();
    r.stacks[0].changes = vec![change(
        "x.y",
        "",
        Action::Add,
        vec![
            leaf("n", LeafOp::Add, "n", "", "\"1\""),
            block(
                "data",
                vec![Variant::new(Level::LineDiff, "+ a\n+ b".to_string())],
            ),
        ],
    )];
    r.stacks[0].counts = counts(|c| c.add = 1);
    let out = render(&r);
    assert!(out.contains(&format!("{META}2 attrs")), "{out}");
    assert!(out.contains("+ a") && out.contains("+ b"), "{out}");
}

#[test]
fn render_block_field_in_resource_body() {
    let mut r = sample_report();
    r.stacks[0].changes = vec![change(
        "kubernetes_config_map.app",
        "kubernetes_config_map",
        Action::Change,
        vec![block(
            "data",
            vec![
                Variant::new(Level::LineDiff, "- old\n+ new".to_string()),
                Variant::new(
                    Level::Summary,
                    "  ~ data · text · 2 lines · 2 changed (hidden to fit size limit)".to_string(),
                ),
                Variant::new(Level::Hidden, String::new()),
            ],
        )],
    )];
    r.stacks[0].counts = counts(|c| c.change = 1);
    let out = render(&r);
    assert!(out.contains("~ data:"), "{out}");
    assert!(out.contains("- old") && out.contains("+ new"));
    assert!(
        !out.contains("<details open><summary>~ data") && !out.contains("<details><summary>~ data")
    );
}

#[test]
fn render_header_links() {
    let mut r = sample_report();
    r.header_links = vec![
        Link {
            label: "PR #1".to_string(),
            url: "https://x/1".to_string(),
        },
        Link {
            label: "Build".to_string(),
            url: "https://x/b".to_string(),
        },
    ];
    assert!(render(&r).contains("[PR #1](https://x/1) · [Build](https://x/b)"));
}

#[test]
fn render_stack_and_resource_links() {
    let mut r = sample_report();
    r.stacks[0].url = "https://x/stack".to_string();
    r.stacks[0].changes[0].url = "https://x/res".to_string();
    let out = render(&r);
    assert!(
        out.contains(r#"<a href="https://x/stack">platform/nonprod</a>"#),
        "{out}"
    );
    assert!(
        out.contains(r#"<a href="https://x/res">google_storage_bucket.tfstate</a>"#),
        "{out}"
    );
}

#[test]
fn per_stack_sections() {
    let mut a = stack("stacks/a", counts(|c| c.add = 1));
    a.changes = vec![change(
        "aws_s3_bucket.a",
        "aws_s3_bucket",
        Action::Add,
        vec![],
    )];
    let mut b = stack("stacks/b", counts(|c| c.change = 1));
    b.changes = vec![change(
        "aws_iam_role.b",
        "aws_iam_role",
        Action::Change,
        vec![],
    )];
    let r = Report {
        title: "T".to_string(),
        marker: "m".to_string(),
        stacks: vec![a, b, stack("stacks/c", Counts::default())],
        ..Report::default()
    };
    let per = per_stack(&r);
    assert_eq!(per.len(), 2);
    assert!(per["stacks/a"].contains("aws_s3_bucket.a"));
    assert!(per["stacks/b"].contains("aws_iam_role.b"));
    assert!(!per.contains_key("stacks/c"));
}

#[test]
fn render_no_table_variant() {
    let r = sample_report();
    let full = render(&r);
    let no_table = render_no_table(&r);
    assert!(full.contains("| Stack |"));
    assert!(!no_table.contains("| Stack |"));
    assert!(no_table.contains("### "));
    assert!(no_table.contains("<details"));
}

#[test]
fn render_empty_report() {
    let r = Report {
        title: "Terraform plan — nonprod".to_string(),
        marker: "tf-plan:nonprod".to_string(),
        classified: true,
        header_links: vec![Link {
            label: "PR #42".to_string(),
            url: "https://gh/o/r/pull/42".to_string(),
        }],
        ..Report::default()
    };
    let out = render(&r);
    assert!(out.starts_with("<!-- tf-plan:nonprod -->\n"));
    assert!(out.contains("### Terraform plan — nonprod  (0 stacks changed)"));
    assert!(!out.contains("| Stack |"));
    assert!(out.contains("[PR #42](https://gh/o/r/pull/42)"));
}

#[test]
fn render_sensitivity_only() {
    let sens = counts(|c| {
        c.change = 7;
        c.sensitivity_only = 7;
    });
    let r_min = Report {
        title: "Terraform plan — nonprod".to_string(),
        marker: "tfstackplan:nonprod".to_string(),
        mode: Mode::Minimal,
        stacks: vec![stack("platform/nonprod", sens)],
        ..Report::default()
    };
    assert!(render(&r_min).contains("7 changes (7 sensitivity-only)"));

    let mut s = stack("platform/nonprod", sens);
    let mut c = change(
        "google_storage_bucket.tfstate",
        "google_storage_bucket",
        Action::Change,
        vec![leaf(
            "labels",
            LeafOp::Add,
            "labels.team",
            "",
            "\"platform\"",
        )],
    );
    c.sensitivity_only = true;
    s.changes = vec![c];
    let r_det = Report {
        title: "Terraform plan — nonprod".to_string(),
        marker: "tfstackplan:nonprod".to_string(),
        stacks: vec![s],
        ..Report::default()
    };
    let out = render(&r_det);
    assert!(out.contains("7 change (7 sensitivity-only)"), "{out}");
    assert!(out.contains("sensitivity change · 1 attrs"), "{out}");
}

#[test]
fn link_urls_are_go_quoted() {
    let mut r = sample_report();
    r.stacks[0].url = "https://x/\"q\"".to_string();
    assert!(render(&r).contains(r#"<a href="https://x/\"q\"">"#));
}
