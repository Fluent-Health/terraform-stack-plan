//! `run wrap`: run one stack command with passthrough stdio and, when a server
//! and execution id are configured, report its start, stream its output as log
//! chunks and report its outcome. It always exits with the command's exit code.
//!
//! The command gets this process's stdin, stdout and stderr through pipes that
//! are copied to the real ones as output arrives (`--tty` instead runs it on a
//! pty, merged into stdout). Like Go, the wrapper installs no signal handlers:
//! Ctrl-C reaches the command and the wrapper alike. A command killed by a
//! signal exits the wrapper with 255, Go's `os.Exit(-1)`.

use std::io::{Read, Write};
use std::process::{Command, ExitCode, Stdio};
use std::sync::Mutex;

use tfstackplan_cli::flag::FlagSet;
use tfstackplan_cli::wrap::{Capture, LogBatcher, classify_outcome, known_status, last_line};
use tfstackplan_clients::server::{Client, LogChunk, Update};

use super::{code, err, parse};
use crate::proc;
use crate::server::{client_from_env, env};

/// Where output goes besides the terminal: the log stream (when reporting) and
/// the capture used to classify the outcome.
struct Sinks<'a> {
    stream: Option<(&'a Client, &'a str, &'a str)>,
    batch: Mutex<LogBatcher>,
    capture: Mutex<Capture>,
}

impl Sinks<'_> {
    fn write(&self, p: &[u8]) {
        if let Some((client, id, stack)) = self.stream {
            let chunk = self.batch.lock().ok().and_then(|mut b| b.write(p));
            if let Some(data) = chunk {
                let _ = client.log_chunk(&LogChunk {
                    id: id.to_string(),
                    stack: stack.to_string(),
                    data,
                });
            }
        }
        if let Ok(mut c) = self.capture.lock() {
            c.write(p);
        }
    }

    fn close(&self) {
        if let Some((client, id, stack)) = self.stream
            && let Some(data) = self.batch.lock().ok().and_then(|mut b| b.flush())
        {
            let _ = client.log_chunk(&LogChunk {
                id: id.to_string(),
                stack: stack.to_string(),
                data,
            });
        }
    }
}

/// Copies `r` to `w` and the sinks until EOF (or EIO, how a pty ends).
fn pump(mut r: impl Read, w: &mut dyn Write, sinks: &Sinks<'_>) {
    let mut buf = [0u8; 32 * 1024];
    loop {
        match r.read(&mut buf) {
            Ok(0) | Err(_) => break,
            Ok(n) => {
                let _ = w.write_all(&buf[..n]);
                let _ = w.flush();
                sinks.write(&buf[..n]);
            }
        }
    }
}

/// Runs `run wrap`.
pub fn run(args: &[String]) -> ExitCode {
    code(wrap(args))
}

/// `run wrap` returning Go's exit code (2 for usage errors).
fn wrap(args: &[String]) -> i32 {
    let mut fs = FlagSet::new("run wrap");
    fs.string("stack", "", "stack path (defaults to $TFSTACKPLAN_STACK)")
        .string(
            "on-success",
            "",
            "status to report on a zero exit (e.g. safe, planned); empty = intermediate, report nothing on success",
        )
        .string("running", "", "status to report on start (default: running)")
        .bool(
            "tty",
            false,
            "run the command under a PTY so it emits ANSI colour (graceful fallback to a pipe if unavailable)",
        );
    let sep = args.iter().position(|a| a == "--");
    let Some(sep) = sep.filter(|s| s + 1 < args.len()) else {
        err("tfstackplan run wrap: expected -- <command...>\n");
        return 2;
    };
    let Ok(p) = parse(&fs, &args[..sep]) else {
        return 2;
    };
    let mut start_status = "running".to_string();
    if !p.str("running").is_empty() {
        if !known_status(p.str("running")) {
            err(&format!(
                "tfstackplan run wrap: unknown --running status: {}\n",
                p.str("running")
            ));
            return 2;
        }
        start_status = p.str("running").to_string();
    }
    let cmd_args = &args[sep + 1..];

    let mut stack = p.str("stack").to_string();
    if stack.is_empty() {
        stack = env("TFSTACKPLAN_STACK");
    }
    let exec_id = env("TFSTACKPLAN_EXECUTION");
    // Go builds the client once to decide and again to report; both print the
    // same audience warning, so this does too.
    let reporting = !stack.is_empty() && !exec_id.is_empty() && client_from_env().enabled();
    let client = if reporting {
        Some(client_from_env())
    } else {
        None
    };
    if let Some(c) = &client {
        let _ = c.update(&Update {
            id: exec_id.clone(),
            stack: stack.clone(),
            status: start_status,
            detail: String::new(),
        });
    }

    let sinks = Sinks {
        stream: client
            .as_ref()
            .map(|c| (c, exec_id.as_str(), stack.as_str())),
        batch: Mutex::new(LogBatcher::default()),
        capture: Mutex::new(Capture::default()),
    };

    let exit = if p.bool("tty") {
        match run_pty(cmd_args, &sinks) {
            Ok(c) => c,
            Err(e) => {
                err(&format!(
                    "tfstackplan run wrap: PTY unavailable, falling back to pipe (no colour): {e}\n"
                ));
                run_piped(cmd_args, &sinks)
            }
        }
    } else {
        run_piped(cmd_args, &sinks)
    };

    if let Some(c) = &client {
        sinks.close();
        let captured = sinks.capture.lock().map(|c| c.text()).unwrap_or_default();
        let clean = tfstackplan_cli::ansi::strip(&captured);
        let status = classify_outcome(exit, &clean, p.str("on-success"));
        if !status.is_empty() {
            let detail = if status == "failed" {
                last_line(&clean)
            } else {
                String::new()
            };
            if let Err(e) = c.update(&Update {
                id: exec_id.clone(),
                stack: stack.clone(),
                status: status.clone(),
                detail,
            }) {
                err(&format!(
                    "tfstackplan run wrap: {stack}: status {} not recorded: {e}\n",
                    tfstackplan_cli::gofmt::quote(&status)
                ));
            }
        }
    }
    exit
}

/// The child's exit code as Go's `ExitError.ExitCode()` gives it: -1 when killed.
fn exit_code(status: std::process::ExitStatus) -> i32 {
    status.code().unwrap_or(-1)
}

/// The original path: separate stdout and stderr pipes, each copied to the real
/// stream and the sinks.
fn run_piped(cmd_args: &[String], sinks: &Sinks<'_>) -> i32 {
    let child = Command::new(&cmd_args[0])
        .args(&cmd_args[1..])
        .stdin(Stdio::inherit())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn();
    let Ok(mut child) = child else {
        // Go's exitCodeOf: a command that never started is 1, silently.
        return 1;
    };
    let (Some(stdout), Some(stderr)) = (child.stdout.take(), child.stderr.take()) else {
        return 1;
    };
    std::thread::scope(|s| {
        s.spawn(|| pump(stdout, &mut std::io::stdout(), sinks));
        s.spawn(|| pump(stderr, &mut std::io::stderr(), sinks));
    });
    child.wait().map(exit_code).unwrap_or(1)
}

/// Runs the command on a pty (it sees a terminal and emits colour); the merged
/// output goes to stdout. Nothing is written to the command's input, as in Go.
fn run_pty(cmd_args: &[String], sinks: &Sinks<'_>) -> Result<i32, String> {
    use portable_pty::{CommandBuilder, PtySize, native_pty_system};
    let pair = native_pty_system()
        .openpty(PtySize {
            rows: 0,
            cols: 0,
            pixel_width: 0,
            pixel_height: 0,
        })
        .map_err(|e| format!("open /dev/ptmx: {e}"))?;
    let mut cb = CommandBuilder::new(&cmd_args[0]);
    cb.args(&cmd_args[1..]);
    if let Ok(cwd) = std::env::current_dir() {
        cb.cwd(cwd);
    }
    let mut child = pair.slave.spawn_command(cb).map_err(|e| {
        let io = e.downcast_ref::<std::io::Error>();
        match io {
            Some(io) => proc::start_error(&cmd_args[0], io),
            None if !cmd_args[0].contains('/') => format!(
                "exec: {}: executable file not found in $PATH",
                tfstackplan_cli::gofmt::quote(&cmd_args[0])
            ),
            None => format!("fork/exec {}: no such file or directory", cmd_args[0]),
        }
    })?;
    // Only the child holds the slave now, so the master reads EOF/EIO when it exits.
    drop(pair.slave);
    let reader = pair
        .master
        .try_clone_reader()
        .map_err(|e| format!("pty reader: {e}"))?;
    pump(reader, &mut std::io::stdout(), sinks);
    drop(pair.master);
    let status = child.wait().map_err(|e| e.to_string())?;
    if status.signal().is_some() {
        return Ok(-1);
    }
    Ok(i32::try_from(status.exit_code()).unwrap_or(1))
}

#[cfg(test)]
mod tests;
