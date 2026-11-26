//! Turns a (possibly fit-reduced) report into the markdown document (`internal/render`).

use std::fmt::Write as _;

use crate::gofmt::quote;
use crate::model::{Action, Change, Counts, Field, Leaf, Level, Mode, Report, Stack};

/// The full document: header, summary table and per-stack details.
pub fn render(r: &Report) -> String {
    render_doc(r, true)
}

/// The document without the summary table.
pub fn render_no_table(r: &Report) -> String {
    render_doc(r, false)
}

fn render_doc(r: &Report, include_table: bool) -> String {
    let mut b = String::new();
    let _ = writeln!(b, "<!-- {} -->", r.marker);
    if r.stacks.is_empty() {
        render_header(&mut b, r);
        return b;
    }
    match r.mode {
        Mode::Minimal => render_minimal(&mut b, r),
        Mode::SummaryOnly | Mode::Full => {
            render_header(&mut b, r);
            if include_table {
                render_table(&mut b, r);
            }
            if !r.notice.is_empty() {
                let _ = write!(b, "\n{}\n", r.notice);
            }
            if r.mode == Mode::Full {
                render_details(&mut b, r);
            }
        }
    }
    b
}

fn changed_stacks(r: &Report) -> usize {
    r.stacks.iter().filter(|s| s.counts.any_change()).count()
}

fn render_header(b: &mut String, r: &Report) {
    let _ = write!(
        b,
        "### {}  ({} stacks changed)\n\n",
        r.title,
        changed_stacks(r)
    );
    if !r.header_links.is_empty() {
        let parts: Vec<String> = r
            .header_links
            .iter()
            .map(|l| format!("[{}]({})", l.label, l.url))
            .collect();
        let _ = write!(b, "{}\n\n", parts.join(" · "));
    }
}

/// A count column: header and the count it shows.
type Column = (&'static str, fn(&Counts) -> i64);

fn render_table(b: &mut String, r: &Report) {
    let any = |f: fn(&Counts) -> i64| r.stacks.iter().any(|s| f(&s.counts) > 0);
    let cols: [Column; 4] = [
        ("Add", |c| c.add),
        ("Change", |c| c.change),
        ("Destroy", |c| c.destroy),
        ("Replace", |c| c.replace),
    ];
    let shown: Vec<Column> = cols.into_iter().filter(|(_, f)| any(*f)).collect();
    let mut headers = vec!["Stack"];
    headers.extend(shown.iter().map(|(h, _)| *h));
    if r.classified {
        headers.push("Categories");
    }
    let _ = writeln!(b, "| {} |", headers.join(" | "));
    let aligns: Vec<&str> = headers
        .iter()
        .map(|h| {
            if matches!(*h, "Stack" | "Categories") {
                "---"
            } else {
                "---:"
            }
        })
        .collect();
    let _ = writeln!(b, "| {} |", aligns.join(" | "));
    for s in &r.stacks {
        let mut name = s.name.clone();
        let extra = extras_parts(&s.counts).join(", ");
        if !extra.is_empty() {
            name.push_str(" · ");
            name.push_str(&extra);
        }
        let mut cells = vec![name];
        cells.extend(shown.iter().map(|(_, f)| f(&s.counts).to_string()));
        if r.classified {
            cells.push(categories_cell(s, r));
        }
        let _ = writeln!(b, "| {} |", cells.join(" | "));
    }
}

fn categories_cell(s: &Stack, r: &Report) -> String {
    if s.categories.is_empty() {
        return r.default.label();
    }
    s.categories
        .iter()
        .map(|c| c.label())
        .collect::<Vec<_>>()
        .join("  ")
}

const OPEN_THRESHOLD: usize = 10;

/// One stack's `<details>` section.
pub fn render_stack_section(b: &mut String, s: &Stack, r: &Report) {
    let blocks: Vec<String> = s
        .changes
        .iter()
        .map(|c| render_resource(c, r.details_open))
        .collect();
    stack_section_from_blocks(b, s, r, &blocks);
}

/// The text a stack section wraps around its rendered resources. The fit tests use it to
/// build a section from resource renders they already have.
pub fn stack_section_from_blocks(b: &mut String, s: &Stack, r: &Report, blocks: &[String]) {
    b.push_str(&stack_section_head(s, r));
    let body = format!("\n{}", blocks.join("\n\n"));
    b.push_str(&blockquote(&body));
    b.push_str("\n</details>\n");
}

/// The opening `<details><summary>` of a stack section.
pub fn stack_section_head(s: &Stack, r: &Report) -> String {
    let name = if s.url.is_empty() {
        s.name.clone()
    } else {
        format!("<a href={}>{}</a>", quote(&s.url), s.name)
    };
    let mut summary = format!("📁&nbsp;<b>{name}</b>");
    if r.classified {
        summary.push_str(" · ");
        summary.push_str(&categories_cell(s, r));
    }
    summary.push_str(" · ");
    summary.push_str(&change_word(&s.counts));
    let open = if r.details_open { " open" } else { "" };
    format!("\n<details{open}><summary>{summary}</summary>\n\n")
}

fn render_details(b: &mut String, r: &Report) {
    for s in &r.stacks {
        if s.counts.any_change() {
            render_stack_section(b, s, r);
        }
    }
}

/// Each changed stack's detail section on its own, by stack name.
pub fn per_stack(r: &Report) -> std::collections::BTreeMap<String, String> {
    r.stacks
        .iter()
        .filter(|s| s.counts.any_change())
        .map(|s| {
            let mut b = String::new();
            render_stack_section(&mut b, s, r);
            (s.name.clone(), b)
        })
        .collect()
}

/// Prefixes every line with "> " (an empty line becomes ">").
pub fn blockquote(s: &str) -> String {
    let s = s.trim_end_matches('\n');
    if s.is_empty() {
        return String::new();
    }
    let lines: Vec<String> = s
        .split('\n')
        .map(|ln| {
            if ln.is_empty() {
                ">".to_string()
            } else {
                format!("> {ln}")
            }
        })
        .collect();
    lines.join("\n") + "\n"
}

/// One resource as a `<details>` row, without a trailing newline.
pub fn render_resource(c: &Change, force_open: bool) -> String {
    let sym = field_sym(c.action);
    let mut leaves: Vec<&Leaf> = Vec::new();
    let mut blocks: Vec<&Field> = Vec::new();
    for f in &c.fields {
        if f.is_block() {
            blocks.push(f);
        } else {
            leaves.extend(&f.leaves);
        }
    }
    let force_sym = if c.action == Action::Forget {
        "⊘"
    } else {
        ""
    };
    let mut body = String::new();
    for line in align_leaves(&leaves, force_sym) {
        body.push_str(&line);
        body.push('\n');
    }
    for f in blocks {
        let v = f.sel();
        if v.level == Level::Hidden || v.content.is_empty() {
            continue;
        }
        let hdr = if f.kind.is_empty() {
            f.name.clone()
        } else {
            format!("{} ({})", f.name, f.kind)
        };
        let _ = writeln!(body, "{sym} {hdr}:\n{}", v.content.trim_end_matches('\n'));
    }
    let mut content = body.trim_end_matches('\n').to_string();
    if content.is_empty() {
        if c.moved {
            content = "(address change only)".to_string();
        } else if c.imported {
            content = "(import only)".to_string();
        }
    }
    let open = if force_open || line_count_of(&content) <= OPEN_THRESHOLD {
        " open"
    } else {
        ""
    };
    format!(
        "<details{open}><summary>{}</summary>\n\n```diff\n{content}\n```\n\n</details>",
        resource_summary(c)
    )
}

const META_INDENT: &str = "&nbsp;&nbsp;&nbsp;&nbsp;";

fn resource_summary(c: &Change) -> String {
    let addr = if c.url.is_empty() {
        c.address.clone()
    } else {
        format!("<a href={}>{}</a>", quote(&c.url), c.address)
    };
    let n = c.fields.len();
    let (glyph, meta) = if c.action == Action::Forget {
        ("⏏️", format!("forgotten · {n} attrs"))
    } else if c.moved {
        let (glyph, mut meta) = match c.move_direction.as_str() {
            "out" => ("↗️", "moved out (relocating)".to_string()),
            "in-update" => ("🚚✏️", "moved in and updated".to_string()),
            "in" => ("↙️", "moved in (cross-state)".to_string()),
            _ if !c.previous_address.is_empty() => {
                ("↪️", format!("moved from {}", c.previous_address))
            }
            _ => ("↪️", "moved in (cross-state)".to_string()),
        };
        if n > 0 && c.move_direction != "in-update" {
            let _ = write!(meta, ", {n} changed");
        }
        (glyph, meta)
    } else if c.imported {
        let mut meta = "imported".to_string();
        if n > 0 {
            let _ = write!(meta, ", {n} changed");
        }
        if !c.import_id.is_empty() {
            let _ = write!(meta, " · id=<code>{}</code>", c.import_id);
        }
        ("📥", meta)
    } else {
        match c.action {
            Action::Add => ("➕", format!("{n} attrs")),
            Action::Destroy => ("➖", format!("{n} attrs")),
            Action::Replace => ("🔁", "replace".to_string()),
            _ if c.sensitivity_only => ("✏️", format!("sensitivity change · {n} attrs")),
            _ => ("✏️", format!("{n} changed")),
        }
    };
    format!("{glyph}&nbsp;{addr}<br>{META_INDENT}{meta}")
}

fn field_sym(a: Action) -> &'static str {
    match a {
        Action::Add => "+",
        Action::Destroy => "-",
        Action::Forget => "⊘",
        _ => "~",
    }
}

fn align_leaves(leaves: &[&Leaf], force_sym: &str) -> Vec<String> {
    let w = leaves.iter().map(|l| l.path.len()).max().unwrap_or(0);
    leaves
        .iter()
        .map(|l| {
            let pad = " ".repeat(w - l.path.len());
            let sym = if force_sym.is_empty() {
                l.op.sym()
            } else {
                force_sym
            };
            format!("{sym} {}{pad} = {}", l.path, l.value())
        })
        .collect()
}

fn line_count_of(s: &str) -> usize {
    if s.is_empty() {
        return 0;
    }
    s.trim_end_matches('\n').matches('\n').count() + 1
}

fn render_minimal(b: &mut String, r: &Report) {
    let mut t = Counts::default();
    for s in &r.stacks {
        let c = &s.counts;
        t.add += c.add;
        t.change += c.change;
        t.destroy += c.destroy;
        t.replace += c.replace;
        t.import += c.import;
        t.moved += c.moved;
        t.forget += c.forget;
        t.sensitivity_only += c.sensitivity_only;
    }
    let mut line = format!(
        "{} stacks · {} adds · {} changes",
        r.stacks.len(),
        t.add,
        t.change
    );
    if t.sensitivity_only > 0 {
        let _ = write!(line, " ({} sensitivity-only)", t.sensitivity_only);
    }
    let _ = write!(line, " · {} destroys · {} replaces", t.destroy, t.replace);
    if t.import + t.moved + t.forget > 0 {
        let _ = write!(
            line,
            " · {} imports · {} moves · {} forgets",
            t.import, t.moved, t.forget
        );
    }
    let _ = write!(b, "### {}\n\n{line}\n", r.title);
    if !r.notice.is_empty() {
        let _ = write!(b, "\n{}\n", r.notice);
    }
}

fn change_word(c: &Counts) -> String {
    let mut parts = Vec::new();
    if c.add > 0 {
        parts.push(format!("{} add", c.add));
    }
    if c.change > 0 {
        if c.sensitivity_only > 0 {
            parts.push(format!(
                "{} change ({} sensitivity-only)",
                c.change, c.sensitivity_only
            ));
        } else {
            parts.push(format!("{} change", c.change));
        }
    }
    if c.destroy > 0 {
        parts.push(format!("{} destroy", c.destroy));
    }
    if c.replace > 0 {
        parts.push(format!("{} replace", c.replace));
    }
    parts.extend(extras_parts(c));
    if parts.is_empty() {
        return "no changes".to_string();
    }
    parts.join(", ")
}

fn extras_parts(c: &Counts) -> Vec<String> {
    let mut parts = Vec::new();
    if c.import > 0 {
        parts.push(format!("{} import", c.import));
    }
    if c.moved > 0 {
        parts.push(format!("{} move", c.moved));
    }
    if c.forget > 0 {
        parts.push(format!("{} forget", c.forget));
    }
    parts
}

#[cfg(test)]
mod tests;
