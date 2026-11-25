//! Records the expected output of every replay case by running the Go binary.
//!
//! For each `cases/<name>.toml` it runs Go twice with the case's args, cwd and
//! stdin (mirroring trycmd's `fs.base` / `fs.cwd` / `fs.sandbox` rules), then
//! writes `<name>.stdout`, `<name>.stderr`, `status.code` in the toml and, for
//! sandboxed cases, the files Go wrote into `<name>.out/`. A line that differs
//! between the two runs keeps its common prefix and suffix with `[..]` between.
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

/// What one Go run produced.
#[derive(PartialEq)]
struct Run {
    code: i32,
    stdout: String,
    stderr: String,
    /// Files the run created or changed in the sandbox, by relative path.
    files: BTreeMap<PathBuf, String>,
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
        })
    };
    let a = run()?;
    let b = run()?;
    if a.code != b.code {
        return Err(format!("exit code differs between runs: {} vs {}", a.code, b.code).into());
    }
    if a.files.keys().ne(b.files.keys()) {
        return Err("the two runs wrote different files".into());
    }

    fs::write(
        toml_path.with_extension("stdout"),
        merge(&a.stdout, &b.stdout)?,
    )?;
    fs::write(
        toml_path.with_extension("stderr"),
        merge(&a.stderr, &b.stderr)?,
    )?;
    if sandbox {
        if out_dir.exists() {
            fs::remove_dir_all(&out_dir)?;
        }
        fs::create_dir_all(&out_dir)?;
        for (rel, content) in &a.files {
            let dest = out_dir.join(rel);
            fs::create_dir_all(dest.parent().ok_or("output path has no parent dir")?)?;
            // trycmd compares written files after turning `\` into `/` (path
            // normalisation) on the actual side only, so record them that way.
            fs::write(dest, merge(content, &b.files[rel])?.replace('\\', "/"))?;
        }
    }
    doc["status"] = toml_edit::table();
    doc["status"]["code"] = toml_edit::value(i64::from(a.code));
    fs::write(toml_path, doc.to_string())?;
    Ok(true)
}

/// Runs Go once. With a sandbox `(base, root)`, also collects every file under
/// `root` that is new or differs from `base`.
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
    let out = Command::new(go)
        .args(args)
        .envs(env.iter().map(|(k, v)| (k, v)))
        .current_dir(cwd)
        .stdin(stdin)
        .output()?;
    let mut files = BTreeMap::new();
    if let Some((base, root)) = sandbox {
        for rel in list_files(root)? {
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
    })
}

/// Merges two runs line by line; a differing line becomes its common prefix,
/// `[..]` and its common suffix.
fn merge(a: &str, b: &str) -> Result<String> {
    if a == b {
        return Ok(a.to_owned());
    }
    let (la, lb): (Vec<&str>, Vec<&str>) = (a.split('\n').collect(), b.split('\n').collect());
    if la.len() != lb.len() {
        return Err("output line count differs between runs; make the case deterministic".into());
    }
    let lines: Vec<String> = la
        .iter()
        .zip(&lb)
        .map(|(x, y)| {
            if x == y {
                x.to_string()
            } else {
                wildcard(x, y)
            }
        })
        .collect();
    Ok(lines.join("\n"))
}

fn wildcard(x: &str, y: &str) -> String {
    let (cx, cy): (Vec<char>, Vec<char>) = (x.chars().collect(), y.chars().collect());
    let pre = cx.iter().zip(&cy).take_while(|(a, b)| a == b).count();
    let max_suf = cx.len().min(cy.len()) - pre;
    let mut suf = cx
        .iter()
        .rev()
        .zip(cy.iter().rev())
        .take(max_suf)
        .take_while(|(a, b)| a == b)
        .count();
    // Widen to whole alphanumeric runs: two random ids may share a first or last
    // digit by chance, and a third run would not.
    let mut pre = pre;
    while pre > 0 && cx[pre - 1].is_alphanumeric() {
        pre -= 1;
    }
    while suf > 0 && cx[cx.len() - suf].is_alphanumeric() {
        suf -= 1;
    }
    let prefix: String = cx[..pre].iter().collect();
    let suffix: String = cx[cx.len() - suf..].iter().collect();
    format!("{prefix}[..]{suffix}")
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
