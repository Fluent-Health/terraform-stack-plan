//! The real [`Runner`](super::exec::Runner): the terraform CLI run the way
//! terraform-exec v0.25.1 runs it — same argv, same environment overrides, and
//! errors read `exit status N` followed by terraform's stderr.

use std::path::Path;
use std::process::{Command, Stdio};

use tfstackplan_cli::statemove::State;
use tfstackplan_cli::statemove::plan::parse_state;

use super::exec::Runner;
use crate::fsx::os_error;

/// terraform-exec's module version, in the user agent it appends.
const TFEXEC_UA: &str = "HashiCorp-terraform-exec/0.25.1";

/// Finds `terraform` on `PATH` like Go's `exec.LookPath`.
pub fn look_path(name: &str) -> Result<String, String> {
    let not_found = || format!("exec: \"{name}\": executable file not found in $PATH");
    let path = std::env::var_os("PATH").ok_or_else(not_found)?;
    for dir in std::env::split_paths(&path) {
        let dir = if dir.as_os_str().is_empty() {
            std::path::PathBuf::from(".")
        } else {
            dir
        };
        let cand = dir.join(name);
        if let Ok(m) = std::fs::metadata(&cand) {
            use std::os::unix::fs::PermissionsExt;
            if m.is_file() && m.permissions().mode() & 0o111 != 0 {
                return Ok(cand.to_string_lossy().into_owned());
            }
        }
    }
    Err(not_found())
}

/// terraform in one working directory.
pub struct Terraform {
    exec_path: String,
    working_dir: String,
}

impl Terraform {
    /// `tfexec.NewTerraform(workingDir, execPath)`: the working directory must exist.
    pub fn new(exec_path: &str, working_dir: &str) -> Result<Self, String> {
        if working_dir.is_empty() {
            return Err("Terraform cannot be initialised with empty workdir".to_string());
        }
        if let Err(e) = std::fs::metadata(working_dir) {
            return Err(format!(
                "error initialising Terraform with workdir {working_dir}: {}",
                os_error("stat", Path::new(working_dir), &e)
            ));
        }
        Ok(Self {
            exec_path: exec_path.to_string(),
            working_dir: working_dir.to_string(),
        })
    }

    fn command(&self, args: &[&str]) -> Command {
        let mut cmd = Command::new(&self.exec_path);
        cmd.args(args).current_dir(&self.working_dir);
        // terraform-exec's buildEnv: keep the caller's env, then override.
        if std::env::var_os("CHECKPOINT_DISABLE").is_none() {
            cmd.env("CHECKPOINT_DISABLE", "");
        }
        let ua = merge_user_agent(&[
            &std::env::var("TF_APPEND_USER_AGENT").unwrap_or_default(),
            TFEXEC_UA,
        ]);
        cmd.env("TF_APPEND_USER_AGENT", ua);
        for k in ["TF_LOG", "TF_LOG_CORE", "TF_LOG_PATH", "TF_LOG_PROVIDER"] {
            cmd.env(k, "");
        }
        cmd.env("TF_IN_AUTOMATION", "1");
        cmd.env_remove("TF_WORKSPACE");
        cmd.stdin(Stdio::null());
        // terraform-exec also sets Setpgid with Pdeathsig=SIGKILL, so terraform dies with
        // its parent. Pdeathsig needs unsafe code, and Setpgid alone would let terraform
        // outlive a cancelled CI job. terraform stays in our process group instead, so
        // the signal that stops us stops it too (DECISIONS.md).
        cmd
    }

    /// Runs terraform and returns stdout; a failure is `exit status N\n<stderr>`.
    fn run(&self, args: &[&str]) -> Result<String, String> {
        let out = self
            .command(args)
            .stdout(Stdio::piped())
            .stderr(Stdio::piped())
            .output()
            .map_err(|e| {
                format!(
                    "fork/exec {}: {}",
                    self.exec_path,
                    crate::gofs::errno_text(&e)
                )
            })?;
        if !out.status.success() {
            let status = match out.status.code() {
                Some(c) => format!("exit status {c}"),
                None => {
                    use std::os::unix::process::ExitStatusExt;
                    format!("signal: {}", signal_name(out.status.signal().unwrap_or(0)))
                }
            };
            return Err(format!(
                "{status}\n{}",
                String::from_utf8_lossy(&out.stderr)
            ));
        }
        Ok(String::from_utf8_lossy(&out.stdout).into_owned())
    }
}

fn signal_name(sig: i32) -> String {
    match sig {
        1 => "hangup".into(),
        2 => "interrupt".into(),
        9 => "killed".into(),
        15 => "terminated".into(),
        n => format!("signal {n}"),
    }
}

/// terraform-exec's `mergeUserAgent`: trimmed, de-duplicated, space-joined.
fn merge_user_agent(uas: &[&str]) -> String {
    let mut merged: Vec<&str> = Vec::new();
    for ua in uas {
        let ua = ua.trim();
        if !ua.is_empty() && !merged.contains(&ua) {
            merged.push(ua);
        }
    }
    merged.join(" ")
}

/// The argv terraform-exec builds for each call (terraform ≥ 0.15).
pub mod argv {
    /// `Init()` with default options.
    pub fn init() -> Vec<String> {
        [
            "init",
            "-no-color",
            "-input=false",
            "-backend=true",
            "-get=true",
            "-upgrade=false",
        ]
        .map(String::from)
        .to_vec()
    }

    /// `StatePull()`.
    pub fn state_pull() -> Vec<String> {
        ["state", "pull"].map(String::from).to_vec()
    }

    /// `StatePush(path, Force(force))`.
    pub fn state_push(path: &str, force: bool) -> Vec<String> {
        let mut a = vec!["state".to_string(), "push".to_string()];
        if force {
            a.push("-force".into());
        }
        a.push("-lock=false".into());
        a.push("-lock-timeout=0s".into());
        a.push(path.into());
        a
    }

    /// `ShowStateFile(path)`.
    pub fn show_state_file(path: &str) -> Vec<String> {
        ["show", "-json", "-no-color", path]
            .map(String::from)
            .to_vec()
    }

    /// `StateMv(from, to, State(state), StateOut(state_out))`.
    pub fn state_mv(from: &str, to: &str, state: &str, state_out: &str) -> Vec<String> {
        vec![
            "state".into(),
            "mv".into(),
            "-no-color".into(),
            "-lock-timeout=0s".into(),
            format!("-state={state}"),
            format!("-state-out={state_out}"),
            "-lock=true".into(),
            from.into(),
            to.into(),
        ]
    }
}

fn strs(v: &[String]) -> Vec<&str> {
    v.iter().map(String::as_str).collect()
}

impl Runner for Terraform {
    fn init(&mut self) -> Result<(), String> {
        self.run(&strs(&argv::init())).map(|_| ())
    }

    fn state_pull(&mut self) -> Result<String, String> {
        self.run(&strs(&argv::state_pull()))
    }

    fn state_push(&mut self, path: &Path, force: bool) -> Result<(), String> {
        self.run(&strs(&argv::state_push(&path.to_string_lossy(), force)))
            .map(|_| ())
    }

    fn show_state_file(&mut self, path: &Path) -> Result<State, String> {
        let out = self.run(&strs(&argv::show_state_file(&path.to_string_lossy())))?;
        parse_state(out.as_bytes())
    }

    fn state_mv(
        &mut self,
        from: &str,
        to: &str,
        state: &Path,
        state_out: &Path,
    ) -> Result<(), String> {
        self.run(&strs(&argv::state_mv(
            from,
            to,
            &state.to_string_lossy(),
            &state_out.to_string_lossy(),
        )))
        .map(|_| ())
    }
}

#[cfg(test)]
mod tests;
