//! Front door for the Go to Rust port of `tfstackplan`.
//!
//! The first argument picks the subcommand. A subcommand listed in the `PORTED` table
//! runs in Rust; anything else runs the Go binary with the same argv, env and
//! stdio, and exits with its exit code. See `rust/README.md`.

#![forbid(unsafe_code)]

use std::ffi::{OsStr, OsString};
use std::io::Write;
use std::path::{Path, PathBuf};
use std::process::ExitCode;

/// A ported subcommand: gets the arguments after the subcommand name.
type Handler = fn(&[OsString]) -> ExitCode;

mod goflag;
mod gofs;
mod render;

/// Subcommands that have a Rust implementation.
const PORTED: &[(&str, Handler)] = &[("render", render::run)];

/// Exit code when the Go binary cannot be found (as a shell does for a missing command).
pub const EXIT_GO_NOT_FOUND: u8 = 127;
/// Exit code when the Go binary is found but cannot be run.
pub const EXIT_GO_NOT_EXECUTABLE: u8 = 126;

/// Runs the front door: dispatches to a ported subcommand or hands the call to
/// the Go binary. The `tfstackplan` binary is only this call.
pub fn main() -> ExitCode {
    let args: Vec<OsString> = std::env::args_os().skip(1).collect();
    let name = subcommand(&args);

    if !forced_to_go(&name, |k| std::env::var_os(k))
        && let Some((_, handler)) = PORTED.iter().find(|(n, _)| *n == name)
    {
        // Go's dispatch treats a leading flag (or no args) as `render` with
        // every argument as a render flag; only strip a named subcommand.
        let rest = if args.first().is_some_and(|a| a == name.as_str()) {
            &args[1..]
        } else {
            &args[..]
        };
        return handler(rest);
    }

    let exe = std::env::current_exe().ok();
    let bin = match resolve_go_bin(std::env::var_os("TFSTACKPLAN_GO_BIN"), exe.as_deref()) {
        Ok(bin) => bin,
        Err((code, msg)) => {
            eprintln!("tfstackplan: {msg}");
            return ExitCode::from(code);
        }
    };
    log_fallback(&name);
    exec(&bin, &args)
}

/// The subcommand Go would dispatch to: the first argument, or `render` when
/// there are no arguments or the first one is a flag.
pub fn subcommand(args: &[OsString]) -> String {
    match args.first() {
        Some(a) if !a.to_string_lossy().starts_with('-') => a.to_string_lossy().into_owned(),
        _ => "render".to_string(),
    }
}

/// Whether `name` has a Rust implementation in this build.
pub fn is_ported(name: &str) -> bool {
    PORTED.iter().any(|(n, _)| *n == name)
}

/// `TFSTACKPLAN_IMPL=go` sends everything to Go; `TFSTACKPLAN_IMPL_<NAME>=go`
/// sends one subcommand to Go. The value is matched case-insensitively. `var`
/// looks a variable up (the process environment, or a case's in tests).
pub fn forced_to_go(name: &str, var: impl Fn(&str) -> Option<OsString>) -> bool {
    let is_go = |key: &str| var(key).is_some_and(|v| v.eq_ignore_ascii_case("go"));
    is_go("TFSTACKPLAN_IMPL") || is_go(&format!("TFSTACKPLAN_IMPL_{}", name.to_uppercase()))
}

/// Finds the Go binary: `env_bin` (the value of `TFSTACKPLAN_GO_BIN`), else
/// `tfstackplan-go` next to `exe` with symlinks resolved. Fails with the exit
/// code and message to print.
fn resolve_go_bin(env_bin: Option<OsString>, exe: Option<&Path>) -> Result<PathBuf, (u8, String)> {
    let real_exe = exe.map(|e| std::fs::canonicalize(e).unwrap_or_else(|_| e.to_path_buf()));
    let bin = match env_bin.filter(|p| !p.is_empty()) {
        Some(p) => {
            let p = PathBuf::from(p);
            // A bare name would be checked against the cwd but found on PATH by exec.
            if !p.is_absolute() && p.parent().is_none_or(|d| d.as_os_str().is_empty()) {
                return Err((
                    EXIT_GO_NOT_FOUND,
                    format!(
                        "TFSTACKPLAN_GO_BIN must be a path, not a bare name: {}",
                        p.display()
                    ),
                ));
            }
            p
        }
        None => {
            let Some(real_exe) = &real_exe else {
                return Err((
                    EXIT_GO_NOT_FOUND,
                    "cannot locate this executable to find tfstackplan-go next to it".into(),
                ));
            };
            real_exe.with_file_name(format!("tfstackplan-go{}", std::env::consts::EXE_SUFFIX))
        }
    };
    if !bin.is_file() {
        return Err((
            EXIT_GO_NOT_FOUND,
            format!(
                "Go binary not found at {} (set TFSTACKPLAN_GO_BIN or install tfstackplan-go next to tfstackplan)",
                bin.display()
            ),
        ));
    }
    if real_exe.is_some_and(|e| std::fs::canonicalize(&bin).is_ok_and(|b| b == e)) {
        return Err((
            EXIT_GO_NOT_EXECUTABLE,
            format!(
                "{} is this front door, not the Go binary; it would run itself forever",
                bin.display()
            ),
        ));
    }
    Ok(bin)
}

/// Appends the subcommand name to `TFSTACKPLAN_FALLBACK_LOG`, if set. Errors are
/// ignored: the log must never change what the command does.
fn log_fallback(name: &str) {
    let Some(path) = std::env::var_os("TFSTACKPLAN_FALLBACK_LOG").filter(|p| !p.is_empty()) else {
        return;
    };
    if let Ok(mut f) = std::fs::OpenOptions::new()
        .create(true)
        .append(true)
        .open(path)
    {
        let _ = writeln!(f, "{name}");
    }
}

/// Replaces this process with the Go binary at exactly `bin`, so signals, the
/// controlling tty and the exit code belong to Go. Only returns if exec fails.
#[cfg(unix)]
fn exec(bin: &Path, args: &[OsString]) -> ExitCode {
    use std::os::unix::process::CommandExt;
    let mut cmd = std::process::Command::new(bin);
    cmd.args(args);
    if let Some(argv0) = std::env::args_os().next() {
        cmd.arg0(argv0);
    }
    let err = cmd.exec();
    fail(bin.as_os_str(), &err)
}

/// Runs the Go binary as a child with inherited stdio and passes its exit code on.
/// Ctrl-C is not forwarded; see `rust/DECISIONS.md`.
#[cfg(not(unix))]
fn exec(bin: &Path, args: &[OsString]) -> ExitCode {
    match std::process::Command::new(bin).args(args).status() {
        Ok(status) => {
            let code = status.code().unwrap_or(1);
            // ExitCode only carries a u8 portably; exit directly to keep the full code.
            std::process::exit(code)
        }
        Err(err) => fail(bin.as_os_str(), &err),
    }
}

fn fail(bin: &OsStr, err: &std::io::Error) -> ExitCode {
    eprintln!("tfstackplan: cannot run {}: {err}", bin.to_string_lossy());
    ExitCode::from(match err.kind() {
        std::io::ErrorKind::NotFound => EXIT_GO_NOT_FOUND,
        _ => EXIT_GO_NOT_EXECUTABLE,
    })
}

#[cfg(test)]
mod tests;
