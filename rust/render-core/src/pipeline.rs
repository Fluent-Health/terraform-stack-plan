//! The render pipeline of `cmd/tfstackplan`'s `run`, without the file system: the caller
//! reads each stack's plan (and source tree) and feeds it in, in stack order.

use std::collections::{BTreeMap, HashMap};

use crate::classify;
use crate::config::Config;
use crate::differ::{self, Input};
use crate::fit;
use crate::gofmt::quote;
use crate::goval::json_string;
use crate::links;
use crate::model::{Category, Change, Counts, Link, Report, Stack};
use crate::moveset::Manifest;
use crate::plan;
use crate::render;
use crate::source::Index;

const MOVE_CATEGORY: &str = "move";
const MOVE_ICON: &str = "🚚";

/// What a finished render produces.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Output {
    /// The markdown report.
    pub markdown: String,
    /// Whether it fits the byte budget.
    pub fits: bool,
    /// The classification sidecar JSON, when the config classifies.
    pub classification_json: Option<String>,
    /// The report without the summary table (what `run` finalizes to the server).
    pub markdown_no_table: String,
    /// Each stack's full plan section, captured before the budget fit.
    pub stack_reports: BTreeMap<String, String>,
}

/// Builds a report one stack at a time.
pub struct Builder<'a> {
    cfg: &'a Config,
    moves: &'a Manifest,
    base: HashMap<String, String>,
    report: Report,
    sidecar: BTreeMap<String, (Vec<Category>, Counts)>,
    all_cats: Vec<Vec<Category>>,
    any_moved: bool,
}

/// Run-level link variables from `key=value` pairs, plus `sha_short` from `sha`.
pub fn base_vars(pairs: &[String]) -> HashMap<String, String> {
    let mut v = HashMap::new();
    for p in pairs {
        if let Some(i) = p.find('=')
            && i > 0
        {
            v.insert(p[..i].to_string(), p[i + 1..].to_string());
        }
    }
    if let Some(sha) = v.get("sha")
        && sha.len() >= 7
    {
        // Go slices bytes; a sha is ASCII.
        let short = String::from_utf8_lossy(&sha.as_bytes()[..7]).into_owned();
        v.insert("sha_short".to_string(), short);
    }
    v
}

impl<'a> Builder<'a> {
    /// Starts a report: title, marker, run-level link variables (`key=value`).
    pub fn new(
        cfg: &'a Config,
        moves: &'a Manifest,
        title: &str,
        marker: &str,
        link_vars: &[String],
    ) -> Self {
        let base = base_vars(link_vars);
        let classified = cfg.classification.is_some();
        let mut report = Report {
            title: title.to_string(),
            marker: marker.to_string(),
            classified,
            ..Report::default()
        };
        if let Some(cl) = &cfg.classification {
            report.default = cl.default.clone();
        }
        if let Some(l) = &cfg.links {
            for h in &l.header {
                let url = links::resolve(&h.url, &base);
                if !url.is_empty() {
                    report.header_links.push(Link {
                        label: links::resolve(&h.label, &base),
                        url,
                    });
                }
            }
        }
        Builder {
            cfg,
            moves,
            base,
            report,
            sidecar: BTreeMap::new(),
            all_cats: Vec::new(),
            any_moved: false,
        }
    }

    /// Whether resource links need a source index for each stack.
    pub fn wants_source(&self) -> bool {
        self.cfg
            .links
            .as_ref()
            .is_some_and(|l| !l.resource.is_empty())
    }

    /// Adds one stack: its name, plan bytes, its directory relative to the repo root (for the
    /// `{stack_dir}` link variable) and, when [`Self::wants_source`], its source index.
    pub fn add_stack(
        &mut self,
        name: &str,
        data: &[u8],
        stack_dir: &str,
        source: Option<&Index>,
    ) -> Result<(), String> {
        let mut raw = plan::parse(name, data)?;
        let targets = self.moves.targets(name);
        let moved_count = raw.apply_state_moves(&targets);
        let mut st = Stack {
            name: name.to_string(),
            url: String::new(),
            counts: raw.counts,
            categories: Vec::new(),
            changes: Vec::new(),
        };
        let mut stack_vars = self.base.clone();
        stack_vars.insert("stack".to_string(), name.to_string());
        stack_vars.insert("stack_dir".to_string(), stack_dir.to_string());
        if let Some(l) = &self.cfg.links {
            st.url = links::resolve(&l.stack, &stack_vars);
        }
        if let Some(cl) = &self.cfg.classification {
            let mut cats = classify::classify(&raw, &cl.rules, &targets);
            if moved_count > 0 {
                cats.push(Category {
                    name: MOVE_CATEGORY.to_string(),
                    icon: MOVE_ICON.to_string(),
                    attributes: None,
                });
                self.any_moved = true;
            }
            st.categories = cats.clone();
            self.sidecar
                .insert(name.to_string(), (cats.clone(), raw.counts));
            self.all_cats.push(cats);
        }
        for rc in std::mem::take(&mut raw.changes) {
            let mut ch = Change {
                address: rc.address.clone(),
                type_: rc.type_.clone(),
                action: rc.action,
                fields: Vec::new(),
                url: String::new(),
                moved: rc.moved,
                move_direction: rc.move_direction.clone(),
                previous_address: rc.previous_address.clone(),
                imported: rc.imported,
                import_id: rc.import_id.clone(),
                sensitivity_only: rc.sensitivity_only,
            };
            for ra in &rc.attrs {
                ch.fields.push(differ::diff(&Input {
                    resource_type: rc.type_.clone(),
                    attr: ra.name.clone(),
                    before: ra.before.clone(),
                    after: ra.after.clone(),
                    sensitive: ra.sensitive,
                    before_sensitive: ra.before_sensitive.clone(),
                    after_sensitive: ra.after_sensitive.clone(),
                    unknown: ra.unknown,
                    force_differ: self.cfg.diff.resolve(&rc.type_, &ra.name),
                    max_lines: self.cfg.diff.max_attribute_lines,
                    no_detect: !self.cfg.diff.detect,
                    sensitivity_only: ra.sensitivity_only,
                }));
            }
            if let Some(l) = &self.cfg.links {
                ch.url = st.url.clone();
                if let Some(loc) =
                    source.and_then(|s| s.lookup(&rc.module_address, &rc.type_, &rc.name))
                {
                    let mut rv = stack_vars.clone();
                    for (k, v) in [
                        ("file", loc.file.clone()),
                        ("line", loc.line.to_string()),
                        ("type", rc.type_.clone()),
                        ("name", rc.name.clone()),
                        ("address", rc.address.clone()),
                        ("module", rc.module_address.clone()),
                    ] {
                        rv.insert(k.to_string(), v);
                    }
                    let u = links::resolve(&l.resource, &rv);
                    if !u.is_empty() {
                        ch.url = u;
                    }
                }
            }
            st.changes.push(ch);
        }
        self.report.stacks.push(st);
        Ok(())
    }

    /// Applies `--details`, fits the budget (`max_bytes <= 0` disables it) and renders.
    pub fn finish(mut self, details: &str, max_bytes: i64) -> Result<Output, String> {
        self.report.details_open = match details {
            "open" => true,
            "" | "closed" => false,
            "auto" => {
                self.report
                    .stacks
                    .iter()
                    .filter(|s| s.counts.any_change())
                    .count()
                    == 1
            }
            other => {
                return Err(format!(
                    "--details must be auto|open|closed, got {}",
                    quote(other)
                ));
            }
        };
        // The per-stack store has no comment-size budget: capture before fitting.
        let stack_reports = render::per_stack(&self.report);
        let fits = fit::fit(&mut self.report, max_bytes);
        let classification_json = self.cfg.classification.as_ref().map(|cl| {
            let mut summary = classify::summarize(&self.all_cats, &cl.rules);
            if self.any_moved {
                summary.push(Category {
                    name: MOVE_CATEGORY.to_string(),
                    icon: MOVE_ICON.to_string(),
                    attributes: None,
                });
            }
            sidecar_json(&self.sidecar, &summary)
        });
        Ok(Output {
            markdown: render::render(&self.report),
            fits,
            classification_json,
            markdown_no_table: render::render_no_table(&self.report),
            stack_reports,
        })
    }
}

/// An ordered JSON value, written like `json.MarshalIndent(v, "", "  ")`.
enum J {
    Null,
    Num(i64),
    Str(String),
    Arr(Vec<J>),
    Obj(Vec<(String, J)>),
}

fn write_j(out: &mut String, v: &J, depth: usize) {
    let nl = |out: &mut String, d: usize| {
        out.push('\n');
        out.push_str(&"  ".repeat(d));
    };
    match v {
        J::Null => out.push_str("null"),
        J::Num(n) => out.push_str(&n.to_string()),
        J::Str(s) => json_string(out, s),
        J::Arr(items) if items.is_empty() => out.push_str("[]"),
        J::Obj(items) if items.is_empty() => out.push_str("{}"),
        J::Arr(items) => {
            out.push('[');
            for (i, item) in items.iter().enumerate() {
                if i > 0 {
                    out.push(',');
                }
                nl(out, depth + 1);
                write_j(out, item, depth + 1);
            }
            nl(out, depth);
            out.push(']');
        }
        J::Obj(items) => {
            out.push('{');
            for (i, (k, item)) in items.iter().enumerate() {
                if i > 0 {
                    out.push(',');
                }
                nl(out, depth + 1);
                json_string(out, k);
                out.push_str(": ");
                write_j(out, item, depth + 1);
            }
            nl(out, depth);
            out.push('}');
        }
    }
}

fn entries(cats: &[Category]) -> J {
    J::Arr(
        cats.iter()
            .map(|c| {
                let mut o = vec![
                    ("category".to_string(), J::Str(c.name.clone())),
                    (
                        "icon".to_string(),
                        if c.icon.is_empty() {
                            J::Null
                        } else {
                            J::Str(c.icon.clone())
                        },
                    ),
                ];
                if let Some(attrs) = c.attributes.as_ref().filter(|a| !a.is_empty()) {
                    o.push((
                        "attributes".to_string(),
                        J::Obj(
                            attrs
                                .iter()
                                .map(|(k, vs)| {
                                    (
                                        k.clone(),
                                        J::Arr(vs.iter().map(|v| J::Str(v.clone())).collect()),
                                    )
                                })
                                .collect(),
                        ),
                    ));
                }
                J::Obj(o)
            })
            .collect(),
    )
}

fn counts_json(c: &Counts) -> J {
    let fields = [
        ("add", c.add),
        ("change", c.change),
        ("destroy", c.destroy),
        ("replace", c.replace),
        ("move", c.moved),
        ("import", c.import),
        ("forget", c.forget),
        ("sensitivity_only", c.sensitivity_only),
    ];
    J::Obj(
        fields
            .iter()
            .filter(|(_, n)| *n != 0)
            .map(|(k, n)| (k.to_string(), J::Num(*n)))
            .collect(),
    )
}

fn sidecar_json(
    stacks: &BTreeMap<String, (Vec<Category>, Counts)>,
    summary: &[Category],
) -> String {
    let doc = J::Obj(vec![
        (
            "stacks".to_string(),
            J::Obj(
                stacks
                    .iter()
                    .map(|(name, (cats, counts))| {
                        (
                            name.clone(),
                            J::Obj(vec![
                                ("categories".to_string(), entries(cats)),
                                ("counts".to_string(), counts_json(counts)),
                            ]),
                        )
                    })
                    .collect(),
            ),
        ),
        (
            "summary".to_string(),
            J::Obj(vec![("categories".to_string(), entries(summary))]),
        ),
    ]);
    let mut out = String::new();
    write_j(&mut out, &doc, 0);
    out
}

#[cfg(test)]
mod tests;
