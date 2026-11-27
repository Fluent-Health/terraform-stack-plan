//! Runs the built front door against a stub Go binary (a shell script).
#![cfg(unix)]
#![expect(
    clippy::unwrap_used,
    reason = "the helpers fail the test by panicking, as the test itself does"
)]

use std::os::unix::fs::PermissionsExt;
use std::path::{Path, PathBuf};
use std::process::Command;

const FRONT_DOOR: &str = env!("CARGO_BIN_EXE_tfstackplan");

fn scratch() -> tempfile::TempDir {
    tempfile::Builder::new()
        .prefix("tfsp-fd-")
        .tempdir()
        .unwrap()
}

/// Writes a script that prints its arguments and exits 3.
fn stub_go(path: &Path, mode: u32) {
    std::fs::write(path, "#!/bin/sh\necho \"go:$*\"\nexit 3\n").unwrap();
    std::fs::set_permissions(path, std::fs::Permissions::from_mode(mode)).unwrap();
}

/// Runs `exe` with a cleared environment plus `vars`.
fn run(exe: &Path, args: &[&str], vars: &[(&str, &Path)]) -> (i32, String, String) {
    let mut cmd = Command::new(exe);
    cmd.args(args).env_clear();
    if let Some(path) = std::env::var_os("PATH") {
        cmd.env("PATH", path);
    }
    for (k, v) in vars {
        cmd.env(k, v);
    }
    let out = cmd.output().unwrap();
    (
        out.status.code().unwrap(),
        String::from_utf8(out.stdout).unwrap(),
        String::from_utf8(out.stderr).unwrap(),
    )
}

// One test, so no other test forks while a stub is still open for writing
// (that makes exec fail with "text file busy").
#[test]
fn hands_off_to_go_and_reports_why_it_cannot() {
    let dir = scratch();
    let go = dir.path().join("tfstackplan-go");
    let log = dir.path().join("fallback.log");
    stub_go(&go, 0o755);

    // Argv and the exit code pass through; the fallback is logged once.
    let (code, stdout, _) = run(
        Path::new(FRONT_DOOR),
        &["render", "--x", "y"],
        &[
            ("TFSTACKPLAN_GO_BIN", &go),
            ("TFSTACKPLAN_FALLBACK_LOG", &log),
        ],
    );
    assert_eq!((code, stdout.as_str()), (3, "go:render --x y\n"));
    assert_eq!(std::fs::read_to_string(&log).unwrap(), "render\n");

    // A missing binary is 127 and logs nothing.
    let missing = dir.path().join("missing");
    let (code, _, stderr) = run(
        Path::new(FRONT_DOOR),
        &["serve"],
        &[
            ("TFSTACKPLAN_GO_BIN", &missing),
            ("TFSTACKPLAN_FALLBACK_LOG", &log),
        ],
    );
    assert_eq!(code, 127, "{stderr}");
    assert_eq!(std::fs::read_to_string(&log).unwrap(), "render\n");

    // A file that is not executable is 126.
    let plain = dir.path().join("plain");
    stub_go(&plain, 0o644);
    let (code, _, stderr) = run(
        Path::new(FRONT_DOOR),
        &[],
        &[("TFSTACKPLAN_GO_BIN", &plain)],
    );
    assert_eq!(code, 126, "{stderr}");

    // Pointing TFSTACKPLAN_GO_BIN at the front door itself is refused.
    let (code, _, stderr) = run(
        Path::new(FRONT_DOOR),
        &[],
        &[("TFSTACKPLAN_GO_BIN", Path::new(FRONT_DOOR))],
    );
    assert_eq!(code, 126, "{stderr}");

    // Installed side by side and reached through a symlink, it finds its sibling.
    let install = dir.path().join("install");
    std::fs::create_dir(&install).unwrap();
    let installed: PathBuf = install.join("tfstackplan");
    std::fs::copy(FRONT_DOOR, &installed).unwrap();
    stub_go(&install.join("tfstackplan-go"), 0o755);
    let bin_dir = dir.path().join("bin");
    std::fs::create_dir(&bin_dir).unwrap();
    let link = bin_dir.join("tfstackplan");
    std::os::unix::fs::symlink(&installed, &link).unwrap();
    let (code, stdout, stderr) = run(&link, &["whoami"], &[]);
    assert_eq!((code, stdout.as_str()), (3, "go:whoami\n"), "{stderr}");
}
