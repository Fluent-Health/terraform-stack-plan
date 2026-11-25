//! Front door for the Go to Rust port of `tfstackplan`.
//!
//! The first argument picks the subcommand. A subcommand listed in the `PORTED` table
//! runs in Rust; anything else runs the Go binary with the same argv, env and
//! stdio, and exits with its exit code. See `rust/README.md`.

#![forbid(unsafe_code)]

use std::ffi::{OsStr, OsString};
use std::io::Write;
use std::path::PathBuf;
use std::process::ExitCode;

mod faces;
mod fsx;
mod proc;
mod server;

/// A ported subcommand: gets the arguments after the subcommand name.
type Handler = fn(&[String]) -> ExitCode;

/// Subcommands that have a Rust implementation.
const PORTED: &[(&str, Handler)] = &[
    ("state", faces::state::run),
    ("claims", faces::claims::run),
    ("catalog", faces::catalog::run),
    ("uniqueness", faces::uniqueness::run),
    ("whoami", faces::whoami::run),
    ("admin", faces::admin::run),
];

/// `run` subcommands that have a Rust implementation. The rest of `run` (the
/// plan/apply orchestrator) still runs in Go.
const PORTED_RUN: &[(&str, Handler)] = &[
    ("wrap", faces::wrap::run),
    ("claims", faces::claims::run),
    ("whoami", faces::whoami::run),
];

/// Runs the front door: dispatches to a ported subcommand or hands the call to
/// the Go binary. The `tfstackplan` binary is only this call.
pub fn main() -> ExitCode {
    let args: Vec<OsString> = std::env::args_os().skip(1).collect();
    let name = subcommand(&args);

    if !forced_to_go(&name)
        && let Some((handler, skip)) = ported(&name, &args)
    {
        // One rustls provider for every HTTP client the faces build.
        tfstackplan_clients::install_crypto_provider();
        // Go reads arguments as strings; a non-UTF-8 byte becomes U+FFFD here.
        let rest: Vec<String> = args[skip..]
            .iter()
            .map(|a| a.to_string_lossy().into_owned())
            .collect();
        return handler(&rest);
    }

    log_fallback(&name);
    run_go(&args)
}

/// The Rust handler for this call, and how many leading arguments name it. Go's
/// dispatch treats a leading flag (or no args) as `render` with every argument as
/// a render flag, so only a named subcommand is stripped.
fn ported(name: &str, args: &[OsString]) -> Option<(Handler, usize)> {
    let named = args.first().is_some_and(|a| a == name);
    if name == "run" && named {
        let sub = args.get(1)?.to_string_lossy();
        let (_, handler) = PORTED_RUN.iter().find(|(n, _)| *n == sub)?;
        return Some((*handler, 2));
    }
    let (_, handler) = PORTED.iter().find(|(n, _)| *n == name)?;
    Some((*handler, usize::from(named)))
}

/// The subcommand Go would dispatch to: the first argument, or `render` when
/// there are no arguments or the first one is a flag.
fn subcommand(args: &[OsString]) -> String {
    match args.first() {
        Some(a) if !a.to_string_lossy().starts_with('-') => a.to_string_lossy().into_owned(),
        _ => "render".to_string(),
    }
}

/// `TFSTACKPLAN_IMPL=go` sends everything to Go; `TFSTACKPLAN_IMPL_<NAME>=go`
/// sends one subcommand to Go.
fn forced_to_go(name: &str) -> bool {
    let is_go = |key: &str| std::env::var_os(key).is_some_and(|v| v == "go");
    is_go("TFSTACKPLAN_IMPL") || is_go(&format!("TFSTACKPLAN_IMPL_{}", name.to_uppercase()))
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

/// `TFSTACKPLAN_GO_BIN`, else `tfstackplan-go` next to this executable.
fn go_bin() -> Option<PathBuf> {
    if let Some(p) = std::env::var_os("TFSTACKPLAN_GO_BIN").filter(|p| !p.is_empty()) {
        return Some(PathBuf::from(p));
    }
    let exe = std::env::current_exe().ok()?;
    Some(exe.with_file_name(format!("tfstackplan-go{}", std::env::consts::EXE_SUFFIX)))
}

fn run_go(args: &[OsString]) -> ExitCode {
    let Some(bin) = go_bin().filter(|p| p.is_file()) else {
        eprintln!(
            "tfstackplan: Go binary not found (set TFSTACKPLAN_GO_BIN or install tfstackplan-go next to tfstackplan)"
        );
        return ExitCode::FAILURE;
    };
    let mut cmd = std::process::Command::new(&bin);
    cmd.args(args);
    exec(cmd, &bin)
}

/// Replaces this process with the Go binary, so signals, the controlling tty
/// and the exit code belong to Go. Only returns if exec fails.
#[cfg(unix)]
fn exec(mut cmd: std::process::Command, bin: &std::path::Path) -> ExitCode {
    use std::os::unix::process::CommandExt;
    if let Some(argv0) = std::env::args_os().next() {
        cmd.arg0(argv0);
    }
    let err = cmd.exec();
    fail(bin.as_os_str(), &err)
}

/// Runs the Go binary as a child with inherited stdio and passes its exit code on.
#[cfg(not(unix))]
fn exec(mut cmd: std::process::Command, bin: &std::path::Path) -> ExitCode {
    match cmd.status() {
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
    ExitCode::FAILURE
}

#[cfg(test)]
mod tests;
