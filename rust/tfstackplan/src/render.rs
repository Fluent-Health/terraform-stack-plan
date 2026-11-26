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

use crate::gofs;
use tfstackplan_cli::flag::{FlagSet, ParseError, Parsed};

pub(crate) const DEFAULT_MAX_BYTES: i64 = 60000;
const PLAN_FILE: &str = "tfplan.json";
const CONFIG_FILE: &str = ".tfstackplan.hcl";

/// The version `-version` prints: set at build time through `TFSTACKPLAN_VERSION`, as Go's
/// release build sets `main.version`.
const VERSION: &str = match option_env!("TFSTACKPLAN_VERSION") {
    Some(v) => v,
    None => "dev",
};

/// The render flag set, declared like Go's `runRender`.
fn flags() -> FlagSet {
    let mut fs = FlagSet::new("tfstackplan render");
    fs.string("plans-dir", "", "directory of per-stack plans (each <stack>/tfplan.json)")
        .string("title", "Terraform plan", "report title")
        .string("marker", "tfstackplan", "HTML-comment marker for CI upsert")
        .string("config", "", "HCL policy file (default: auto-discover .tfstackplan.hcl)")
        .int("max-bytes", DEFAULT_MAX_BYTES, "document byte budget (0 disables)")
        .string("output", "-", "output file ('-' = stdout)")
        .string("emit-classification-json", "", "write computed classes as JSON")
        .string("details", "closed", "details disclosure: auto|open|closed")
        .string("repo-root", ".", "repo root for computing link file paths")
        .string(
            "state-moves",
            "",
            "JSON manifest of pending cross-state move targets per stack ({\"<stack>\":[\"<addr>\",...]}); their planned creates classify as moves (non-iam). Keys must match the --plans-dir stack name.",
        )
        .list(
            "link-var",
            "link template variable as key=value (repeatable); sha=<sha> also derives sha_short",
        )
        .bool("version", false, "print version and exit");
    fs
}

/// The render options (Go's `opts`); `run`'s classify pass builds one too.
pub(crate) struct Opts {
    pub(crate) plans_dir: String,
    pub(crate) title: String,
    pub(crate) marker: String,
    pub(crate) config: String,
    pub(crate) max_bytes: i64,
    pub(crate) output: String,
    pub(crate) class_json: String,
    pub(crate) details: String,
    pub(crate) repo_root: String,
    pub(crate) state_moves: String,
    pub(crate) link_vars: Vec<String>,
}

fn opts(p: &Parsed) -> Opts {
    Opts {
        plans_dir: p.str("plans-dir").to_string(),
        title: p.str("title").to_string(),
        marker: p.str("marker").to_string(),
        config: p.str("config").to_string(),
        max_bytes: p.int("max-bytes"),
        output: p.str("output").to_string(),
        class_json: p.str("emit-classification-json").to_string(),
        details: p.str("details").to_string(),
        repo_root: p.str("repo-root").to_string(),
        state_moves: p.str("state-moves").to_string(),
        link_vars: p.list("link-var").to_vec(),
    }
}

/// Runs `render` with the arguments after the subcommand name and returns Go's exit code:
/// 0 ok, 1 error, 2 bad flags or over budget.
pub fn run(args: &[OsString]) -> ExitCode {
    let args: Vec<String> = args
        .iter()
        .map(|a| a.to_string_lossy().into_owned())
        .collect();
    let values = match flags().parse(&args) {
        Ok(p) => p,
        Err(e) => {
            eprint!("{}", e.stderr());
            // Go's runRender treats -h as success, any other flag error as 2.
            return if matches!(e, ParseError::Help(_)) {
                ExitCode::SUCCESS
            } else {
                ExitCode::from(2)
            };
        }
    };
    if values.bool("version") {
        return match write_stdout(format!("tfstackplan {VERSION}\n").as_bytes()) {
            Ok(()) => ExitCode::SUCCESS,
            Err(code) => code,
        };
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
        if let Err(code) = write_stdout(out.markdown.as_bytes()) {
            return code;
        }
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

/// Exit status a shell reports for a process killed by SIGPIPE (128 + 13).
const SIGPIPE_STATUS: u8 = 141;

/// Writes to stdout as Go's `fmt.Print` does: other write errors are ignored, but a closed
/// pipe ends the process. Go dies of SIGPIPE there; this crate forbids the unsafe code that
/// re-raising the signal would need, so it exits with the status a shell shows for that
/// death instead (see DECISIONS.md).
fn write_stdout(data: &[u8]) -> Result<(), ExitCode> {
    let mut stdout = std::io::stdout().lock();
    match stdout.write_all(data).and_then(|()| stdout.flush()) {
        Err(e) if e.kind() == std::io::ErrorKind::BrokenPipe => Err(ExitCode::from(SIGPIPE_STATUS)),
        _ => Ok(()),
    }
}

/// The whole pipeline, in Go's order, so the first error is the same one Go reports.
pub(crate) fn render(o: &Opts) -> Result<Output, String> {
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
pub(crate) fn discover(dir: &str) -> Option<String> {
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
