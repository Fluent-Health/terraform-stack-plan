//! Records the expected output of every replay case by running the Go binary.
//!
//! For each `cases/<name>.toml` it runs Go three times with the case's args, cwd,
//! stdin and env (mirroring trycmd's `fs.base` / `fs.cwd` / `fs.sandbox` rules),
//! then writes `<name>.stdout`, `<name>.stderr`, `status.code` and
//! `env.inherit = false` in the toml and, for sandboxed cases, the files Go
//! created or changed into `<name>.out/` and the base files it deleted into the
//! toml's `deletes` list.
//!
//! Go runs with a cleared environment: `PATH`, `HOME` pointed at an empty temp
//! dir, and the case's `env.add`. Any difference between the runs fails the
//! recording, unless the case sets `wildcards = true`: then a differing line keeps
//! its common prefix and suffix with `[..]` between, and the varying part may be
//! at most [`MAX_WILDCARD`] characters. Go output that already contains one of
//! trycmd's matching tokens ([`TRYCMD_TOKENS`], or a line that is only `...`)
//! fails the recording, since trycmd would read it as a wildcard.
//!
//! Every run gets the hermetic environment and, if the case has a `[harness]`
//! table, its fake server (see the `casekit` crate). The fake server's URL is
//! recorded as `[FAKE]`. A case whose `harness.requires` names a program that is
//! not on PATH is skipped and its recording left as it is.
//!
//! Usage: `cargo run -p recorder [-- <case-name>...]` from `rust/`, with
//! `TFSTACKPLAN_GO_BIN` pointing at the Go binary.

#![forbid(unsafe_code)]

use std::collections::BTreeMap;
use std::fs;
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};

type Result<T> = std::result::Result<T, Box<dyn std::error::Error>>;

/// How many times each case runs; their outputs must agree.
const SAMPLES: usize = 3;
/// The longest text a `[..]` may stand for, so a wildcard never hides a real difference.
const MAX_WILDCARD: usize = 40;
/// Text trycmd matches loosely in an expected output: `[..]` stands for any text
/// on a line, and the rest are its default redactions. A line that is only `...`
/// stands for any number of lines.
const TRYCMD_TOKENS: &[&str] = &["[..]", "[CWD]", "[ROOT]", "[EXE]"];

/// What one Go run produced.
#[derive(PartialEq)]
struct Run {
    code: i32,
    stdout: String,
    stderr: String,
    /// Files the run created or changed in the sandbox, by relative path.
    files: BTreeMap<PathBuf, String>,
    /// Base files the run deleted from the sandbox, recorded as `deletes`.
    deleted: Vec<PathBuf>,
}

fn main() {
    if let Err(e) = record_all() {
        eprintln!("recorder: {e}");
        std::process::exit(1);
    }
}

fn record_all() -> Result<()> {
    let go = PathBuf::from(
        std::env::var_os("TFSTACKPLAN_GO_BIN").ok_or("set TFSTACKPLAN_GO_BIN to the Go binary")?,
    );
    let go = fs::canonicalize(&go).map_err(|e| format!("{}: {e}", go.display()))?;
    let cases_dir = Path::new(env!("CARGO_MANIFEST_DIR")).join("../cases");
    let only: Vec<String> = std::env::args().skip(1).collect();

    let mut tomls: Vec<PathBuf> = fs::read_dir(&cases_dir)?
        .map(|e| e.map(|e| e.path()))
        .collect::<std::io::Result<_>>()?;
    tomls.retain(|p| p.extension().is_some_and(|e| e == "toml"));
    tomls.sort();

    for toml in tomls {
        let name = toml
            .file_stem()
            .ok_or("case file has no name")?
            .to_string_lossy()
            .into_owned();
        if !only.is_empty() && !only.contains(&name) {
            continue;
        }
        if record(&go, &toml).map_err(|e| format!("{name}: {e}"))? {
            println!("recorded {name}");
        }
    }
    Ok(())
}

/// Records one case; `false` when it was skipped.
fn record(go: &Path, toml_path: &Path) -> Result<bool> {
    let mut doc: toml_edit::DocumentMut = fs::read_to_string(toml_path)?.parse()?;
    let dir = toml_path.parent().ok_or("case file has no parent dir")?;
    if doc
        .get("env")
        .and_then(|t| t.get("inherit"))
        .and_then(|v| v.as_bool())
        == Some(true)
    {
        return Err("cases run with a cleared environment; remove env.inherit = true".into());
    }
    let mut harness = casekit::Harness::from_case(&doc)?;
    // trycmd's own `[env] add` applies to Go too (after the harness env).
    if let Some(add) = doc
        .get("env")
        .and_then(|e| e.get("add"))
        .and_then(|a| a.as_table_like())
    {
        for (k, v) in add.iter() {
            let v = v.as_str().ok_or("env.add values must be strings")?;
            harness.env.push((k.to_string(), v.to_string()));
        }
    }
    let missing = harness.missing_tools();
    if !missing.is_empty() {
        println!(
            "skipped {}: {} not on PATH",
            toml_path.file_stem().unwrap_or_default().to_string_lossy(),
            missing.join(", ")
        );
        return Ok(false);
    }

    let args: Vec<String> = doc
        .get("args")
        .and_then(|a| a.as_array())
        .ok_or("`args` must be an array of strings")?
        .iter()
        .map(|v| {
            v.as_str()
                .map(str::to_owned)
                .ok_or("`args` must be strings")
        })
        .collect::<std::result::Result<_, _>>()?;
    let fs_str = |key: &str| {
        doc.get("fs")
            .and_then(|t| t.get(key))
            .and_then(|v| v.as_str())
            .map(|s| dir.join(s))
    };

    // Same defaulting as trycmd's case loader.
    let in_dir = toml_path.with_extension("in");
    let out_dir = toml_path.with_extension("out");
    let cwd = fs_str("cwd");
    let base = fs_str("base").or_else(|| in_dir.exists().then_some(in_dir).or(cwd.clone()));
    let cwd = cwd
        .or(base.clone())
        .ok_or("set fs.cwd (or add a .in/ dir)")?;
    let sandbox = doc
        .get("fs")
        .and_then(|t| t.get("sandbox"))
        .and_then(|v| v.as_bool())
        .unwrap_or(out_dir.exists());
    let stdin = toml_path.with_extension("stdin");
    let stdin = stdin.exists().then_some(stdin);
    let wildcards = doc
        .get("wildcards")
        .and_then(|v| v.as_bool())
        .unwrap_or(false);

    let run_in_session = |go: &Path,
                          args: &[String],
                          cwd: &Path,
                          stdin: Option<&Path>,
                          session: &casekit::Session|
     -> Result<Run> {
        if !sandbox {
            return run_go(go, args, cwd, stdin, &session.env, None);
        }
        let base = base
            .as_deref()
            .ok_or("a sandbox needs fs.base or a .in/ dir")?;
        let tmp = tempfile::Builder::new()
            .prefix("tfsp-recorder-")
            .tempdir()?;
        copy_dir(base, tmp.path())?;
        let rel = cwd
            .strip_prefix(base)
            .map_err(|e| format!("fs.cwd must be inside fs.base: {e}"))?;
        run_go(
            go,
            args,
            &tmp.path().join(rel),
            stdin,
            &session.env,
            Some((base, tmp.path())),
        )
    };
    let run = || -> Result<Run> {
        let session = harness.start()?;
        let run = run_in_session(go, &args, &cwd, stdin.as_deref(), &session)?;
        Ok(Run {
            code: run.code,
            stdout: session.redact(&run.stdout),
            stderr: session.redact(&run.stderr),
            files: run
                .files
                .into_iter()
                .map(|(k, v)| (k, session.redact(&v)))
                .collect(),
            deleted: run.deleted,
        })
    };
    let runs = (0..SAMPLES).map(|_| run()).collect::<Result<Vec<_>>>()?;
    let first = &runs[0];
    if runs.iter().any(|r| r.code != first.code) {
        let codes: Vec<i32> = runs.iter().map(|r| r.code).collect();
        return Err(format!("exit code differs between Go runs: {codes:?}").into());
    }
    if runs.iter().any(|r| r.files.keys().ne(first.files.keys())) {
        return Err("the Go runs wrote different files".into());
    }
    if runs.iter().any(|r| r.deleted != first.deleted) {
        return Err("the Go runs deleted different files".into());
    }
    let pick = |f: &dyn Fn(&Run) -> &str| runs.iter().map(f).collect::<Vec<_>>();

    fs::write(
        toml_path.with_extension("stdout"),
        merge("stdout", &pick(&|r| &r.stdout), wildcards)?,
    )?;
    fs::write(
        toml_path.with_extension("stderr"),
        merge("stderr", &pick(&|r| &r.stderr), wildcards)?,
    )?;
    if sandbox {
        if out_dir.exists() {
            fs::remove_dir_all(&out_dir)?;
        }
        fs::create_dir_all(&out_dir)?;
        for rel in first.files.keys() {
            let dest = out_dir.join(rel);
            fs::create_dir_all(dest.parent().ok_or("output path has no parent dir")?)?;
            // trycmd compares written files after turning `\` into `/` (path
            // normalisation) on the actual side only, so record them that way.
            let what = rel.display().to_string();
            let merged = merge(&what, &pick(&|r| &r.files[rel]), wildcards)?;
            fs::write(dest, merged.replace('\\', "/"))?;
        }
    }
    // `.out/` cannot hold a deletion, so deleted base files are listed in the toml
    // (trycmd ignores the key; the replay checks it).
    doc.remove("deletes");
    if !first.deleted.is_empty() {
        let list: toml_edit::Array = first
            .deleted
            .iter()
            .map(|p| p.to_string_lossy().replace('\\', "/"))
            .collect();
        doc["deletes"] = toml_edit::value(list);
    }
    doc["status"] = toml_edit::table();
    doc["status"]["code"] = toml_edit::value(i64::from(first.code));
    if doc.get("env").is_none() {
        doc["env"] = toml_edit::table();
    }
    doc["env"]["inherit"] = toml_edit::value(false);
    fs::write(toml_path, doc.to_string())?;
    Ok(true)
}

/// Runs Go once. With a sandbox `(base, root)`, also collects every file under
/// `root` that is new or differs from `base`, and every base file it deleted.
fn run_go(
    go: &Path,
    args: &[String],
    cwd: &Path,
    stdin: Option<&Path>,
    env: &[(String, String)],
    sandbox: Option<(&Path, &Path)>,
) -> Result<Run> {
    let stdin = match stdin {
        Some(p) => Stdio::from(fs::File::open(p)?),
        None => Stdio::null(),
    };
    // A cleared environment: only PATH, then the session's HOME, harness env and
    // the case's env.add.
    let mut cmd = Command::new(go);
    cmd.args(args).current_dir(cwd).stdin(stdin).env_clear();
    if let Some(path) = std::env::var_os("PATH") {
        cmd.env("PATH", path);
    }
    cmd.envs(env.iter().map(|(k, v)| (k, v)));
    let out = cmd.output()?;
    let mut files = BTreeMap::new();
    let mut deleted = Vec::new();
    if let Some((base, root)) = sandbox {
        let after = list_files(root)?;
        deleted = list_files(base)?
            .into_iter()
            .filter(|p| !after.contains(p))
            .collect();
        deleted.sort();
        for rel in after {
            let now = fs::read(root.join(&rel))?;
            if fs::read(base.join(&rel)).ok().as_ref() != Some(&now) {
                files.insert(rel, String::from_utf8(now)?);
            }
        }
    }
    Ok(Run {
        code: out.status.code().ok_or("Go was killed by a signal")?,
        stdout: String::from_utf8(out.stdout)?,
        stderr: String::from_utf8(out.stderr)?,
        files,
        deleted,
    })
}

/// Merges the runs' `what` output line by line. Identical runs give the output
/// itself. Otherwise, with `wildcards`, a differing line becomes its common prefix,
/// `[..]` and its common suffix, if the varying part is short; without, it fails.
fn merge(what: &str, runs: &[&str], wildcards: bool) -> Result<String> {
    for run in runs {
        no_trycmd_tokens(what, run)?;
    }
    let first = runs[0];
    if runs.iter().all(|r| *r == first) {
        return Ok(first.to_owned());
    }
    let lines: Vec<Vec<&str>> = runs.iter().map(|r| r.split('\n').collect()).collect();
    if lines.iter().any(|l| l.len() != lines[0].len()) {
        return Err(format!(
            "{what}: line count differs between Go runs; make the case deterministic"
        )
        .into());
    }
    let mut out = Vec::with_capacity(lines[0].len());
    for i in 0..lines[0].len() {
        let variants: Vec<&str> = lines.iter().map(|l| l[i]).collect();
        if variants.iter().all(|v| *v == variants[0]) {
            out.push(variants[0].to_owned());
            continue;
        }
        if !wildcards {
            return Err(format!(
                "{what} line {} differs between Go runs: {variants:?}; make the case deterministic, or set `wildcards = true` in its toml",
                i + 1
            )
            .into());
        }
        out.push(wildcard(&variants).map_err(|e| format!("{what} line {}: {e}", i + 1))?);
    }
    Ok(out.join("\n"))
}

/// Fails if Go's `output` holds text trycmd would treat as a wildcard, so a
/// recording never matches more than Go printed.
fn no_trycmd_tokens(what: &str, output: &str) -> Result<()> {
    for (i, line) in output.split('\n').enumerate() {
        let token = TRYCMD_TOKENS
            .iter()
            .copied()
            .find(|t| line.contains(t))
            .or((line == "...").then_some("..."));
        if let Some(token) = token {
            return Err(format!(
                "{what} line {}: Go printed {token}, which trycmd would read as a wildcard; change the case so Go doesn't print it",
                i + 1
            )
            .into());
        }
    }
    Ok(())
}

/// The common prefix and suffix of `variants` around `[..]`, if the part that
/// varies is at most [`MAX_WILDCARD`] characters in every variant.
fn wildcard(variants: &[&str]) -> Result<String> {
    let chars: Vec<Vec<char>> = variants.iter().map(|v| v.chars().collect()).collect();
    let shortest = chars.iter().map(Vec::len).min().unwrap_or(0);
    let same_at = |f: &dyn Fn(&Vec<char>) -> char| chars.iter().all(|c| f(c) == f(&chars[0]));
    let mut pre = (0..shortest).take_while(|&i| same_at(&|c| c[i])).count();
    let mut suf = (0..shortest - pre)
        .take_while(|&i| same_at(&|c| c[c.len() - 1 - i]))
        .count();
    // Widen to whole alphanumeric runs: random ids may share a first or last
    // character by chance, and another run would not.
    let c = &chars[0];
    while pre > 0 && c[pre - 1].is_alphanumeric() {
        pre -= 1;
    }
    while suf > 0 && c[c.len() - suf].is_alphanumeric() {
        suf -= 1;
    }
    let widest = chars.iter().map(|c| c.len() - pre - suf).max().unwrap_or(0);
    if widest > MAX_WILDCARD {
        return Err(format!(
            "{widest} characters differ between Go runs, more than a [..] may cover ({MAX_WILDCARD}): {variants:?}"
        )
        .into());
    }
    let prefix: String = c[..pre].iter().collect();
    let suffix: String = c[c.len() - suf..].iter().collect();
    Ok(format!("{prefix}[..]{suffix}"))
}

fn list_files(root: &Path) -> Result<Vec<PathBuf>> {
    let mut out = Vec::new();
    let mut stack = vec![PathBuf::new()];
    while let Some(rel) = stack.pop() {
        for entry in fs::read_dir(root.join(&rel))? {
            let entry = entry?;
            let path = rel.join(entry.file_name());
            if entry.file_type()?.is_dir() {
                stack.push(path);
            } else {
                out.push(path);
            }
        }
    }
    Ok(out)
}

fn copy_dir(from: &Path, to: &Path) -> Result<()> {
    for rel in list_files(from)? {
        let dest = to.join(&rel);
        fs::create_dir_all(dest.parent().ok_or("output path has no parent dir")?)?;
        fs::copy(from.join(&rel), dest)?;
    }
    Ok(())
}

#[cfg(test)]
mod tests;
