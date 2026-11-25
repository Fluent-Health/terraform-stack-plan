//! `tfstackplan render` in Rust: Go's flags, the plans-dir walk, config discovery and every
//! file read and write. The pipeline itself is the `tfstackplan-render` crate.

use std::ffi::OsString;
use std::io::Write;
use std::process::ExitCode;

use tfstackplan_render::config::{self, Config};
use tfstackplan_render::gofmt::quote;
use tfstackplan_render::moveset::Manifest;
use tfstackplan_render::source::{self, Index};
use tfstackplan_render::{Builder, Output};

use crate::goflag::{self, Kind, Stop};
use crate::gofs;

const DEFAULT_MAX_BYTES: i64 = 60000;
const PLAN_FILE: &str = "tfplan.json";
const CONFIG_FILE: &str = ".tfstackplan.hcl";

/// The version `-version` prints: set at build time through `TFSTACKPLAN_VERSION`, as Go's
/// release build sets `main.version`.
const VERSION: &str = match option_env!("TFSTACKPLAN_VERSION") {
    Some(v) => v,
    None => "dev",
};

const FLAGS: &[(&str, Kind)] = &[
    ("plans-dir", Kind::Str),
    ("title", Kind::Str),
    ("marker", Kind::Str),
    ("config", Kind::Str),
    ("max-bytes", Kind::Int),
    ("output", Kind::Str),
    ("emit-classification-json", Kind::Str),
    ("details", Kind::Str),
    ("repo-root", Kind::Str),
    ("state-moves", Kind::Str),
    ("link-var", Kind::List),
    ("version", Kind::Bool),
];

/// Go's usage text for the render flag set, byte for byte.
const USAGE: &str = r#"Usage of tfstackplan render:
  -config string
    	HCL policy file (default: auto-discover .tfstackplan.hcl)
  -details string
    	details disclosure: auto|open|closed (default "closed")
  -emit-classification-json string
    	write computed classes as JSON
  -link-var value
    	link template variable as key=value (repeatable); sha=<sha> also derives sha_short
  -marker string
    	HTML-comment marker for CI upsert (default "tfstackplan")
  -max-bytes int
    	document byte budget (0 disables) (default 60000)
  -output string
    	output file ('-' = stdout) (default "-")
  -plans-dir string
    	directory of per-stack plans (each <stack>/tfplan.json)
  -repo-root string
    	repo root for computing link file paths (default ".")
  -state-moves string
    	JSON manifest of pending cross-state move targets per stack ({"<stack>":["<addr>",...]}); their planned creates classify as moves (non-iam). Keys must match the --plans-dir stack name.
  -title string
    	report title (default "Terraform plan")
  -version
    	print version and exit
"#;

struct Opts {
    plans_dir: String,
    title: String,
    marker: String,
    config: String,
    max_bytes: i64,
    output: String,
    class_json: String,
    details: String,
    repo_root: String,
    state_moves: String,
    link_vars: Vec<String>,
}

fn opts(v: &goflag::Values) -> Opts {
    let s = |name: &str, def: &str| {
        v.get(name)
            .and_then(|x| x.last())
            .cloned()
            .unwrap_or_else(|| def.to_string())
    };
    Opts {
        plans_dir: s("plans-dir", ""),
        title: s("title", "Terraform plan"),
        marker: s("marker", "tfstackplan"),
        config: s("config", ""),
        max_bytes: v
            .get("max-bytes")
            .and_then(|x| x.last())
            .and_then(|x| goflag::parse_int(x).ok())
            .unwrap_or(DEFAULT_MAX_BYTES),
        output: s("output", "-"),
        class_json: s("emit-classification-json", ""),
        details: s("details", "closed"),
        repo_root: s("repo-root", "."),
        state_moves: s("state-moves", ""),
        link_vars: v.get("link-var").cloned().unwrap_or_default(),
    }
}

/// Runs `render` with the arguments after the subcommand name and returns Go's exit code:
/// 0 ok, 1 error, 2 bad flags or over budget.
pub fn run(args: &[OsString]) -> ExitCode {
    let args: Vec<String> = args
        .iter()
        .map(|a| a.to_string_lossy().into_owned())
        .collect();
    let values = match goflag::parse(&args, FLAGS) {
        Ok((v, _rest)) => v,
        Err(Stop::Help) => {
            eprint!("{USAGE}");
            return ExitCode::SUCCESS;
        }
        Err(Stop::Error(msg)) => {
            eprint!("{msg}\n{USAGE}");
            return ExitCode::from(2);
        }
    };
    if values
        .get("version")
        .and_then(|v| v.last())
        .is_some_and(|v| v == "true")
    {
        println!("tfstackplan {VERSION}");
        return ExitCode::SUCCESS;
    }
    let o = opts(&values);
    let out = match render(&o) {
        Ok(out) => out,
        Err(e) => {
            eprintln!("tfstackplan: {e}");
            return ExitCode::FAILURE;
        }
    };
    if o.output == "-" || o.output.is_empty() {
        let mut stdout = std::io::stdout().lock();
        let _ = stdout.write_all(out.markdown.as_bytes());
        let _ = stdout.flush();
    } else if let Err(e) = gofs::write_file(&o.output, out.markdown.as_bytes()) {
        eprintln!("tfstackplan: {e}");
        return ExitCode::FAILURE;
    }
    if !out.fits {
        eprintln!("tfstackplan: warning: report exceeds --max-bytes even after full reduction");
        return ExitCode::from(2);
    }
    ExitCode::SUCCESS
}

/// The whole pipeline, in Go's order, so the first error is the same one Go reports.
fn render(o: &Opts) -> Result<Output, String> {
    if o.plans_dir.is_empty() {
        return Err("no input: pass --plans-dir".to_string());
    }
    let stacks = scan(&o.plans_dir)?;
    let moves = if o.state_moves.is_empty() {
        Manifest::default()
    } else {
        let data = gofs::read_file(&o.state_moves)
            .map_err(|e| format!("read state-moves {}: {e}", quote(&o.state_moves)))?;
        Manifest::parse(&o.state_moves, &data)?
    };
    let cfg_path = if o.config.is_empty() {
        discover(".").unwrap_or_default()
    } else {
        o.config.clone()
    };
    let cfg = if cfg_path.is_empty() {
        Config::default()
    } else {
        let text = gofs::read_file(&cfg_path)?;
        config::load(&cfg_path, &String::from_utf8_lossy(&text))?
    };

    let mut b = Builder::new(&cfg, &moves, &o.title, &o.marker, &o.link_vars);
    for (name, plan_path) in &stacks {
        let data = gofs::read_file(plan_path).map_err(|e| format!("stack {}: {e}", quote(name)))?;
        let stack_dir = gofs::join(&[&o.repo_root, name]);
        let source = b
            .wants_source()
            .then(|| build_index(&stack_dir, &o.repo_root));
        let rel_dir = rel_slash(&o.repo_root, &stack_dir);
        b.add_stack(name, &data, &rel_dir, source.as_ref())?;
    }
    let out = b.finish(&o.details, o.max_bytes)?;
    if !o.class_json.is_empty()
        && let Some(json) = &out.classification_json
    {
        gofs::write_file(&o.class_json, json.as_bytes())?;
    }
    Ok(out)
}

/// `plandir.Scan`: one stack per `tfplan.json` below `dir`, named by its directory relative to
/// `dir`, sorted by name.
fn scan(dir: &str) -> Result<Vec<(String, String)>, String> {
    let meta = std::fs::metadata(dir).map_err(|e| gofs::path_error("stat", dir, &e))?;
    if !meta.is_dir() {
        return Err(format!("plans-dir {} is not a directory", quote(dir)));
    }
    let mut stacks = Vec::new();
    // filepath.WalkDir does not follow a symlinked root.
    if std::fs::symlink_metadata(dir).is_ok_and(|m| m.is_dir()) {
        walk(dir, dir, &mut stacks)?;
    }
    stacks.sort_by(|a, b| a.0.cmp(&b.0));
    Ok(stacks)
}

fn walk(root: &str, path: &str, stacks: &mut Vec<(String, String)>) -> Result<(), String> {
    let entries = gofs::read_dir(path).map_err(|e| gofs::path_error("open", path, &e))?;
    for (name, is_dir) in entries {
        let child = gofs::join(&[path, &name]);
        if is_dir {
            walk(root, &child, stacks)?;
            continue;
        }
        if name != PLAN_FILE {
            continue;
        }
        let parent = gofs::join(&[path]);
        let rel = gofs::rel(&abs_or_self(root), &abs_or_self(&parent)).unwrap_or_default();
        if rel == "." {
            return Err(format!(
                "plandir: {PLAN_FILE} sits at the scan root; plans must live in a subdirectory (<stack>/{PLAN_FILE})"
            ));
        }
        stacks.push((rel, child));
    }
    Ok(())
}

fn abs_or_self(p: &str) -> String {
    gofs::abs(p).unwrap_or_else(|| gofs::clean(p))
}

/// `config.Discover`: the nearest `.tfstackplan.hcl` at or above `dir`, stopping at the repo
/// root (the first directory with a `.git` entry).
fn discover(dir: &str) -> Option<String> {
    let mut d = gofs::abs(dir).unwrap_or_else(|| dir.to_string());
    loop {
        let p = gofs::join(&[&d, CONFIG_FILE]);
        if gofs::is_regular_file(&p) {
            return Some(p);
        }
        if gofs::exists(&gofs::join(&[&d, ".git"])) {
            return None;
        }
        let parent = match d.rfind('/') {
            Some(0) => "/".to_string(),
            Some(i) => d[..i].to_string(),
            None => ".".to_string(),
        };
        if parent == d {
            return None;
        }
        d = parent;
    }
}

/// `target` relative to `root` as a slash path, or "" when that fails.
fn rel_slash(root: &str, target: &str) -> String {
    match (gofs::abs(root), gofs::abs(target)) {
        (Some(r), Some(t)) => gofs::rel(&r, &t).unwrap_or_default(),
        _ => String::new(),
    }
}

/// `target` relative to `root`, or `None` when it escapes `root`.
fn rel_inside(root: &str, target: &str) -> Option<String> {
    let rel = gofs::rel(&gofs::abs(root)?, &gofs::abs(target)?)?;
    if rel == ".." || rel.starts_with("../") {
        return None;
    }
    Some(rel)
}

/// `source.Build`: the stack's root module plus its local modules from
/// `.terraform/modules/modules.json`. Never fails; unreadable parts are skipped.
fn build_index(dir: &str, repo_root: &str) -> Index {
    let mut idx = Index::default();
    parse_dir(&mut idx, dir, "", repo_root);
    let mj = gofs::join(&[dir, ".terraform", "modules", "modules.json"]);
    if let Ok(data) = gofs::read_file(&mj)
        && let Some(modules) = source::parse_modules_json(&data)
    {
        for (key, mdir) in modules {
            if key.is_empty() || source::under_dot_terraform(&mdir) {
                continue;
            }
            let md = if mdir.starts_with('/') {
                mdir
            } else {
                gofs::join(&[dir, &mdir])
            };
            parse_dir(&mut idx, &md, &key, repo_root);
        }
    }
    idx
}

fn parse_dir(idx: &mut Index, dir: &str, module_key: &str, repo_root: &str) {
    let Ok(entries) = gofs::read_dir(dir) else {
        return;
    };
    for (name, is_dir) in entries {
        if is_dir || !name.ends_with(".tf") {
            continue;
        }
        let full = gofs::join(&[dir, &name]);
        let Ok(src) = gofs::read_file(&full) else {
            continue;
        };
        let Ok(text) = String::from_utf8(src) else {
            continue;
        };
        let Some(resources) = source::resource_blocks(&text) else {
            continue;
        };
        let Some(rel) = rel_inside(repo_root, &full) else {
            continue;
        };
        idx.add_file(module_key, &rel, resources);
    }
}

#[cfg(test)]
mod tests;
