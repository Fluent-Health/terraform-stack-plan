//! Replays every recorded case in `rust/cases/` against the front door and
//! writes `rust/cases/replay-report.md`.
//!
//! Each case runs in the hermetic environment, with its `[harness]` fake server
//! if it has one (see the `casekit` crate). A case that needs a program that is
//! not on PATH is skipped and listed in the report.
//!
//! Needs the Go binary: `TFSTACKPLAN_GO_BIN`, else `rust/.go/tfstackplan-go`.

use std::path::{Path, PathBuf};

#[test]
fn replay() {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("..");
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

    // Removed when the test ends, even if it fails.
    let scratch = tempfile::Builder::new()
        .prefix("tfsp-replay-")
        .tempdir()
        .unwrap();
    let log = scratch.path().join("fallback.log");

    let mut cases: Vec<PathBuf> = std::fs::read_dir(&cases_dir)
        .unwrap()
        .map(|e| e.unwrap().path())
        .filter(|p| p.extension().is_some_and(|e| e == "toml"))
        .collect();
    cases.sort();
    assert!(!cases.is_empty(), "no cases in {}", cases_dir.display());

    // One TestCases per case, so a failure is counted instead of ending the run.
    let mut failed = Vec::new();
    let mut skipped = Vec::new();
    for case in &cases {
        let name = case.file_stem().unwrap().to_string_lossy().into_owned();
        let doc: toml_edit::DocumentMut = std::fs::read_to_string(case).unwrap().parse().unwrap();
        let harness = casekit::Harness::from_case(&doc).unwrap();
        let missing = harness.missing_tools();
        if !missing.is_empty() {
            eprintln!("skipping {name}: {} not on PATH", missing.join(", "));
            skipped.push(format!("{name} ({} not on PATH)", missing.join(", ")));
            continue;
        }
        let session = harness.start().unwrap();
        let ok = std::panic::catch_unwind(|| {
            let t = trycmd::TestCases::new();
            t.register_bin("tfstackplan", trycmd::cargo::cargo_bin!("tfstackplan"))
                .env("TFSTACKPLAN_GO_BIN", go.to_string_lossy())
                .env("TFSTACKPLAN_FALLBACK_LOG", log.to_string_lossy());
            for (k, v) in &session.env {
                t.env(k, v);
            }
            if let (Some(url), Some(host)) = (session.fake_url(), session.fake_host()) {
                t.insert_var(casekit::FAKE_VAR, url).unwrap();
                t.insert_var(casekit::FAKE_HOST_VAR, host).unwrap();
            }
            t.case(case).run();
        })
        .is_ok();
        if !ok {
            failed.push(name);
        }
    }

    let fallbacks = std::fs::read_to_string(&log)
        .unwrap_or_default()
        .lines()
        .count();
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
        for name in &failed {
            report.push_str(&format!("- {name}\n"));
        }
    }
    std::fs::write(cases_dir.join("replay-report.md"), report).unwrap();

    assert!(failed.is_empty(), "replay failed: {failed:?}");
}
