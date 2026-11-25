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

/// Go's `ExitError` text: `exit status N`, or `signal: <name>`.
pub fn exit_text(status: &std::process::ExitStatus) -> String {
    match status.code() {
        Some(c) => format!("exit status {c}"),
        None => {
            #[cfg(unix)]
            {
                use std::os::unix::process::ExitStatusExt;
                format!(
                    "signal: {}",
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
        9 => "killed".into(),
        13 => "broken pipe".into(),
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
        .map_err(|e| fail(start_error("terramate", &e), b""))?;
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
