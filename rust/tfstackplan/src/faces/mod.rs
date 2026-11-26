//! The ported subcommands. Each `run` takes the arguments after the subcommand
//! name and returns the exit code, writing to stdout and stderr exactly as Go does.

use std::io::Write;
use std::process::ExitCode;

use tfstackplan_cli::flag::{FlagSet, Parsed};

pub mod admin;
pub mod catalog;
pub mod claims;
pub mod state;
pub mod uniqueness;
pub mod whoami;
pub mod wrap;

/// Writes to stdout. A closed pipe is ignored, as Go ignores the write error.
pub fn out(s: &str) {
    let mut o = std::io::stdout().lock();
    let _ = o.write_all(s.as_bytes());
    let _ = o.flush();
}

/// Writes to stderr.
pub fn err(s: &str) {
    let _ = std::io::stderr().lock().write_all(s.as_bytes());
}

/// Parses flags like Go's `fs.Parse` under `ContinueOnError`: on any error
/// (including `-h`) prints what Go prints and yields exit code 2.
pub fn parse(fs: &FlagSet, args: &[String]) -> Result<Parsed, ExitCode> {
    fs.parse(args).map_err(|e| {
        err(e.stderr());
        ExitCode::from(2)
    })
}

/// An exit code in Go's `os.Exit` sense: the low 8 bits.
pub fn code(c: i32) -> ExitCode {
    ExitCode::from((c & 0xff) as u8)
}
