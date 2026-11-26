//! Replays every recorded case in `rust/cases/` against the front door and
//! writes `rust/replay-report.md`.
//!
//! Each case runs with a cleared environment (`env.inherit = false` is required):
//! `PATH`, the hermetic variables and empty `HOME` from `casekit`, the Go binary,
//! a fallback log of its own and the case's `env.add`. Beyond trycmd's output checks, each case must:
//! - fall back to Go exactly when its subcommand is not ported (or a kill switch
//!   in its env sends it there), checked through the fallback log;
//! - when sandboxed, leave exactly its base files, less its `deletes`, plus its
//!   `.out/` files, with every base file not in `.out/` unchanged;
//! - when not sandboxed, leave the repository checkout as it found it.
//!
//! A case with a `[harness]` table gets its fake server (see the `casekit`
//! crate). A case that needs a program that is not on PATH is skipped and listed
//! in the report.
//!
//! Needs the Go binary: `TFSTACKPLAN_GO_BIN`, else `rust/.go/tfstackplan-go`.
#![expect(
    clippy::unwrap_used,
    clippy::unwrap_in_result,
    reason = "the helpers fail the test by panicking, as the test itself does"
)]

use std::collections::{BTreeMap, BTreeSet};
use std::ffi::OsString;
use std::path::{Path, PathBuf};
use std::process::Command;
use std::time::SystemTime;

const FRONT_DOOR: &str = env!("CARGO_BIN_EXE_tfstackplan");

/// What the replay needs from a case toml.
struct Case {
    name: String,
    args: Vec<String>,
    env_add: BTreeMap<String, String>,
    cwd: PathBuf,
    /// The sandbox base and the cwd inside it, for sandboxed cases.
    sandbox: Option<(PathBuf, PathBuf)>,
    out_dir: PathBuf,
    /// Base files a sandboxed run deletes (the toml's `deletes`).
    deletes: BTreeSet<PathBuf>,
    harness: casekit::Harness,
}

fn load(path: &Path) -> Result<Case, String> {
    let doc: toml_edit::DocumentMut = std::fs::read_to_string(path)
        .map_err(|e| e.to_string())?
        .parse()
        .map_err(|e: toml_edit::TomlError| e.to_string())?;
    let dir = path.parent().ok_or("no parent dir")?;
    if doc
        .get("env")
        .and_then(|t| t.get("inherit"))
        .and_then(|v| v.as_bool())
        != Some(false)
    {
        return Err("needs `env.inherit = false` (re-record it: cargo run -p recorder)".into());
    }
    let args = doc
        .get("args")
        .and_then(|a| a.as_array())
        .ok_or("`args` must be an array")?
        .iter()
        .map(|v| {
            v.as_str()
                .map(str::to_owned)
                .ok_or("`args` must be strings")
        })
        .collect::<Result<_, _>>()?;
    let env_add = doc
        .get("env")
        .and_then(|t| t.get("add"))
        .and_then(|t| t.as_table_like())
        .map(|t| {
            t.iter()
                .filter_map(|(k, v)| Some((k.to_string(), v.as_str()?.to_string())))
                .collect()
        })
        .unwrap_or_default();
    let fs_get = |key: &str| doc.get("fs").and_then(|t| t.get(key));
    let fs_path = |key: &str| fs_get(key).and_then(|v| v.as_str()).map(|s| dir.join(s));
    let in_dir = path.with_extension("in");
    let out_dir = path.with_extension("out");
    let cwd = fs_path("cwd");
    let base = fs_path("base").or_else(|| in_dir.exists().then_some(in_dir).or(cwd.clone()));
    let cwd = cwd.or(base.clone()).ok_or("set fs.cwd")?;
    let sandboxed = fs_get("sandbox")
        .and_then(|v| v.as_bool())
        .unwrap_or(out_dir.exists());
    let sandbox = match (sandboxed, base) {
        (true, Some(base)) => {
            let rel = cwd
                .strip_prefix(&base)
                .map_err(|e| format!("fs.cwd must be inside fs.base: {e}"))?
                .to_path_buf();
            Some((base, rel))
        }
        (true, None) => return Err("a sandbox needs fs.base".into()),
        (false, _) => None,
    };
    Ok(Case {
        name: path
            .file_stem()
            .ok_or("no name")?
            .to_string_lossy()
            .into_owned(),
        args,
        env_add,
        cwd,
        sandbox,
        out_dir,
        deletes: doc
            .get("deletes")
            .and_then(|v| v.as_array())
            .map(|a| {
                a.iter()
                    .filter_map(|v| v.as_str())
                    .map(PathBuf::from)
                    .collect()
            })
            .unwrap_or_default(),
        harness: casekit::Harness::from_case(&doc)?,
    })
}

/// Every file under `root` with its size and mtime, skipping build output and
/// version control.
fn tree(root: &Path) -> BTreeMap<PathBuf, (u64, Option<SystemTime>)> {
    const SKIP: &[&str] = &[".git", "target", ".go", "node_modules"];
    let mut out = BTreeMap::new();
    let mut stack = vec![PathBuf::new()];
    while let Some(rel) = stack.pop() {
        let Ok(entries) = std::fs::read_dir(root.join(&rel)) else {
            continue;
        };
        for entry in entries.flatten() {
            let name = entry.file_name();
            if SKIP.iter().any(|s| name == *s) {
                continue;
            }
            let path = rel.join(&name);
            let Ok(meta) = entry.metadata() else { continue };
            if meta.is_dir() {
                stack.push(path);
            } else {
                out.insert(path, (meta.len(), meta.modified().ok()));
            }
        }
    }
    out
}

fn files(root: &Path) -> BTreeSet<PathBuf> {
    tree(root).into_keys().collect()
}

/// Runs the front door for `case` in `cwd` with the replay environment.
fn run_front_door(case: &Case, cwd: &Path, vars: &[(String, OsString)]) {
    let mut cmd = Command::new(FRONT_DOOR);
    cmd.args(&case.args).current_dir(cwd).env_clear();
    if let Some(path) = std::env::var_os("PATH") {
        cmd.env("PATH", path);
    }
    cmd.envs(vars.iter().map(|(k, v)| (k, v)))
        .envs(&case.env_add);
    cmd.output().unwrap();
}

/// The checks trycmd does not make. Returns why the case failed, if it did.
fn extra_checks(case: &Case, log: &Path, vars: &[(String, OsString)]) -> Option<String> {
    let args: Vec<OsString> = case.args.iter().map(OsString::from).collect();
    let name = tfstackplan::subcommand(&args);
    let forced = tfstackplan::forced_to_go(&name, |k| case.env_add.get(k).map(OsString::from));
    let expect_go = forced || !tfstackplan::is_ported(&args);
    let logged = std::fs::read_to_string(log).unwrap_or_default();
    let want = if expect_go {
        format!("{name}\n")
    } else {
        String::new()
    };
    if logged != want {
        return Some(format!(
            "fallback log {logged:?}, expected {want:?} ({name} should run in {})",
            if expect_go { "Go" } else { "Rust" }
        ));
    }

    let (base, rel) = case.sandbox.as_ref()?;
    // trycmd's sandbox is gone by now, so run the case once more in our own copy.
    let sandbox = tempfile::Builder::new()
        .prefix("tfsp-sandbox-")
        .tempdir()
        .unwrap();
    for f in files(base) {
        let dest = sandbox.path().join(&f);
        std::fs::create_dir_all(dest.parent().unwrap()).unwrap();
        std::fs::copy(base.join(&f), dest).unwrap();
    }
    run_front_door(case, &sandbox.path().join(rel), vars);
    let mut want = files(base);
    want.retain(|f| !case.deletes.contains(f));
    want.extend(files(&case.out_dir));
    let got = files(sandbox.path());
    if got != want {
        return Some(format!(
            "sandbox files differ from base - deletes + .out: extra {:?}, missing {:?}",
            got.difference(&want).collect::<Vec<_>>(),
            want.difference(&got).collect::<Vec<_>>()
        ));
    }
    // Base files Go left alone must come out byte for byte the same.
    let out = files(&case.out_dir);
    files(base)
        .into_iter()
        .filter(|f| !case.deletes.contains(f) && !out.contains(f))
        .find(|f| std::fs::read(sandbox.path().join(f)).ok() != std::fs::read(base.join(f)).ok())
        .map(|f| format!("base file changed by Rust but not by Go: {}", f.display()))
}

#[test]
fn replay() {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("..");
    let repo = std::fs::canonicalize(root.join("..")).unwrap();
    let cases_dir = root.join("cases");
    let go = std::env::var_os("TFSTACKPLAN_GO_BIN")
        .map(PathBuf::from)
        .unwrap_or_else(|| root.join(".go/tfstackplan-go"));
    assert!(
        go.is_file(),
        "Go binary not found at {}: build it with `CGO_ENABLED=0 go build -o rust/.go/tfstackplan-go ./cmd/tfstackplan` or set TFSTACKPLAN_GO_BIN",
        go.display()
    );
    let go = std::fs::canonicalize(go).unwrap();
    let path_var = std::env::var("PATH").unwrap_or_default();

    // Removed when the test ends, even if it fails.
    let scratch = tempfile::Builder::new()
        .prefix("tfsp-replay-")
        .tempdir()
        .unwrap();

    let mut cases: Vec<PathBuf> = std::fs::read_dir(&cases_dir)
        .unwrap()
        .map(|e| e.unwrap().path())
        .filter(|p| p.extension().is_some_and(|e| e == "toml"))
        .collect();
    cases.sort();
    assert!(!cases.is_empty(), "no cases in {}", cases_dir.display());

    let mut failed: Vec<(String, String)> = Vec::new();
    let mut skipped = Vec::new();
    let mut fallbacks = 0;
    for (i, path) in cases.iter().enumerate() {
        let stem = path.file_stem().unwrap().to_string_lossy().into_owned();
        let case = match load(path) {
            Ok(case) => case,
            Err(e) => {
                failed.push((stem, e));
                continue;
            }
        };
        let missing = case.harness.missing_tools();
        if !missing.is_empty() {
            eprintln!("skipping {stem}: {} not on PATH", missing.join(", "));
            skipped.push(format!("{stem} ({} not on PATH)", missing.join(", ")));
            continue;
        }
        let session = case.harness.start().unwrap();
        let log = scratch.path().join(format!("fallback-{i}.log"));
        let mut vars: Vec<(String, OsString)> = session
            .env
            .iter()
            .map(|(k, v)| (k.clone(), v.into()))
            .collect();
        vars.push(("TFSTACKPLAN_GO_BIN".into(), go.clone().into()));
        vars.push(("TFSTACKPLAN_FALLBACK_LOG".into(), log.clone().into()));
        let before = case.sandbox.is_none().then(|| tree(&repo));

        // One TestCases per case, so a failure is counted instead of ending the run.
        let ok = std::panic::catch_unwind(|| {
            let cases = trycmd::TestCases::new();
            cases.register_bin("tfstackplan", Path::new(FRONT_DOOR));
            // A case's own env.add wins: trycmd would let these override it.
            let mine = |k: &str| case.env_add.contains_key(k);
            if !mine("PATH") {
                cases.env("PATH", &path_var);
            }
            for (k, v) in vars.iter().filter(|(k, _)| !mine(k)) {
                cases.env(k, v.to_string_lossy());
            }
            if let (Some(url), Some(host)) = (session.fake_url(), session.fake_host()) {
                cases.insert_var(casekit::FAKE_VAR, url).unwrap();
                cases.insert_var(casekit::FAKE_HOST_VAR, host).unwrap();
            }
            cases.case(path).run();
        })
        .is_ok();
        fallbacks += std::fs::read_to_string(&log)
            .unwrap_or_default()
            .lines()
            .count();

        let mut why = (!ok).then(|| "output differs (see above)".to_string());
        if why.is_none() {
            why = extra_checks(&case, &log, &vars);
        }
        if why.is_none()
            && let Some(before) = before
            && tree(&repo) != before
        {
            why = Some(format!(
                "dirtied the checkout (cwd {}); move it into a sandbox",
                case.cwd.display()
            ));
        }
        if let Some(why) = why {
            failed.push((case.name, why));
        }
    }

    let total = cases.len() - skipped.len();
    let passed = total - failed.len();
    let mut report = format!(
        "# Replay report\n\n\
         Written by `cargo test` (tests/replay.rs). Do not edit by hand.\n\n\
         | | |\n|---|---|\n\
         | Cases passed | {passed}/{total} |\n\
         | Fallbacks to Go | {fallbacks}/{total} |\n\
         | Rust-handled | {}/{total} |\n",
        total.saturating_sub(fallbacks)
    );
    if !skipped.is_empty() {
        report.push_str("\n## Skipped\n\n");
        for name in &skipped {
            report.push_str(&format!("- {name}\n"));
        }
    }
    if !failed.is_empty() {
        report.push_str("\n## Failed\n\n");
        for (name, why) in &failed {
            report.push_str(&format!("- {name}: {why}\n"));
        }
    }
    std::fs::write(root.join("replay-report.md"), report).unwrap();

    assert!(failed.is_empty(), "replay failed: {failed:#?}");
}
