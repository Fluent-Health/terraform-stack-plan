//! Running helper programs (terramate) the way Go's `exec.Command` does, with
//! Go's error wording.

use std::path::Path;
use std::process::Command;

/// Go's text for a failed start: `exec: "x": executable file not found in $PATH`
/// for a bare name, `fork/exec <path>: <errno>` otherwise.
pub fn start_error(bin: &str, e: &std::io::Error) -> String {
    if !bin.contains('/') && e.kind() == std::io::ErrorKind::NotFound {
        format!(
            "exec: {}: executable file not found in $PATH",
            tfstackplan_cli::gofmt::quote(bin)
        )
    } else {
        format!("fork/exec {bin}: {}", crate::gofs::errno_text(e))
    }
}

/// Go's text for a failed start in working directory `dir`: a missing program
/// still wins (Go looks it up first), then `chdir <dir>: <errno>` when the
/// directory is unusable, else [`start_error`].
pub fn start_error_in(bin: &str, dir: &Path, e: &std::io::Error) -> String {
    let found = bin.contains('/') || crate::faces::state::terraform::look_path(bin).is_ok();
    let chdir = match std::fs::metadata(dir) {
        Err(m) => Some(crate::gofs::errno_text(&m)),
        Ok(m) if !m.is_dir() => Some("not a directory".to_string()),
        Ok(_) => None,
    };
    match chdir {
        Some(why) if found => format!("chdir {}: {why}", dir.display()),
        _ => start_error(bin, e),
    }
}

/// Go's `ExitError` text: `exit status N`, or `signal: <name>`, with
/// ` (core dumped)` when the process dumped core.
pub fn exit_text(status: &std::process::ExitStatus) -> String {
    match status.code() {
        Some(c) => format!("exit status {c}"),
        None => {
            #[cfg(unix)]
            {
                use std::os::unix::process::ExitStatusExt;
                let core = if status.core_dumped() {
                    " (core dumped)"
                } else {
                    ""
                };
                format!(
                    "signal: {}{core}",
                    signal_name(status.signal().unwrap_or_default())
                )
            }
            #[cfg(not(unix))]
            {
                "exit status 1".to_string()
            }
        }
    }
}

/// The names Go's `syscall.Signal.String` gives the common signals.
pub fn signal_name(sig: i32) -> String {
    match sig {
        1 => "hangup".into(),
        2 => "interrupt".into(),
        3 => "quit".into(),
        6 => "aborted".into(),
        7 => "bus error".into(),
        9 => "killed".into(),
        10 => "user defined signal 1".into(),
        11 => "segmentation fault".into(),
        12 => "user defined signal 2".into(),
        13 => "broken pipe".into(),
        14 => "alarm clock".into(),
        15 => "terminated".into(),
        n => format!("signal {n}"),
    }
}

/// `runner.Terramate.output`: run `terramate <args>` in `dir`, returning stdout, or
/// `terramate <args>: <err>: <stderr>`.
pub fn terramate(dir: &Path, args: &[&str]) -> Result<Vec<u8>, String> {
    let fail = |e: String, stderr: &[u8]| {
        format!(
            "terramate {}: {e}: {}",
            args.join(" "),
            String::from_utf8_lossy(stderr)
        )
    };
    let out = Command::new("terramate")
        .args(args)
        .current_dir(dir)
        .output()
        .map_err(|e| fail(start_error_in("terramate", dir, &e), b""))?;
    if !out.status.success() {
        return Err(fail(exit_text(&out.status), &out.stderr));
    }
    Ok(out.stdout)
}

/// Trimmed, non-empty lines (Go's `runner.lines`).
pub fn lines(b: &[u8]) -> Vec<String> {
    String::from_utf8_lossy(b)
        .split('\n')
        .map(str::trim)
        .filter(|l| !l.is_empty())
        .map(str::to_owned)
        .collect()
}

#[cfg(test)]
mod tests;
