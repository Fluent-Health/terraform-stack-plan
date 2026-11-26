//! A closed stdout ends `render` the way it ends Go: no panic, and the status a shell shows
//! for a process killed by SIGPIPE (141). See DECISIONS.md.

#![cfg(unix)]
#![expect(
    clippy::unwrap_used,
    reason = "the helpers run only inside this test, where a panic is how it fails"
)]

use std::path::Path;
use std::process::{Command, Stdio};

/// Runs the front door with a stdout pipe whose read end is already closed.
fn run_with_closed_stdout(args: &[&str]) -> (Option<i32>, String) {
    let (reader, writer) = std::io::pipe().unwrap();
    drop(reader);
    let out = Command::new(env!("CARGO_BIN_EXE_tfstackplan"))
        .current_dir(Path::new(env!("CARGO_MANIFEST_DIR")).join("../.."))
        .env_remove("TFSTACKPLAN_IMPL")
        .env_remove("TFSTACKPLAN_IMPL_RENDER")
        .args(args)
        .stdout(writer)
        .stderr(Stdio::piped())
        .output()
        .unwrap();
    (
        out.status.code(),
        String::from_utf8_lossy(&out.stderr).into_owned(),
    )
}

#[test]
fn version_to_closed_stdout_exits_141_without_panic() {
    let (code, stderr) = run_with_closed_stdout(&["render", "-version"]);
    assert_eq!(code, Some(141));
    assert_eq!(stderr, "");
}

#[test]
fn report_to_closed_stdout_exits_141_without_warning() {
    // Over budget, so a completed write would be followed by Go's warning and exit 2.
    let (code, stderr) = run_with_closed_stdout(&[
        "render",
        "--plans-dir",
        "rust/cases/fixtures/big",
        "--max-bytes",
        "200",
    ]);
    assert_eq!(code, Some(141));
    assert_eq!(stderr, "");
}
