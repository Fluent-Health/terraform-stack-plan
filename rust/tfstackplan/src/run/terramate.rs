//! The terramate driver (Go's `runner.Terramate`): every call runs `terramate`
//! with the project root as its working directory, as Go does (no `--chdir`).

use std::path::PathBuf;
use std::process::{Command, Stdio};

use tfstackplan_cli::run::{ChangeReason, Edge, parse_why_line};

use crate::proc;

/// What `terramate script run` is asked to do.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct ScriptOpts {
    /// The script name (`plan`, `apply`, …).
    pub script: String,
    /// `--changed`: only changed stacks.
    pub changed: bool,
    /// `--parallel N` (0: terramate's default).
    pub parallel: i64,
    /// `-B <ref>` for change detection.
    pub base: String,
}

impl ScriptOpts {
    /// The argv after `terramate`.
    pub fn args(&self) -> Vec<String> {
        let mut a = vec!["script".to_string(), "run".to_string()];
        if self.changed {
            a.push("--changed".into());
        }
        if self.parallel > 0 {
            a.push("--parallel".into());
            a.push(self.parallel.to_string());
        }
        if !self.base.is_empty() {
            a.push("-B".into());
            a.push(self.base.clone());
        }
        a.push(self.script.clone());
        a
    }
}

/// The terramate operations `run` drives; a trait so the orchestration is tested
/// without terramate or terraform.
pub trait Terramate {
    /// `terramate list --changed [-B base]`.
    fn changed_stacks(&self, base: &str) -> Result<Vec<String>, String>;
    /// `terramate list`.
    fn list(&self) -> Result<Vec<String>, String>;
    /// `terramate list --changed --why [-B base]`.
    fn why_changed(&self, base: &str) -> Result<Vec<ChangeReason>, String>;
    /// `terramate experimental run-graph -l stack.dir`, as dependency edges.
    fn run_graph(&self) -> Result<Vec<Edge>, String>;
    /// `terramate script run …`, its output on this process's stderr, with `env`
    /// added to the environment.
    fn script_run(&self, o: &ScriptOpts, env: &[(String, String)]) -> Result<(), String>;
}

/// The real driver for a project root.
pub struct Real {
    dir: PathBuf,
}

impl Real {
    /// The driver for `dir`.
    pub fn new(dir: &str) -> Self {
        Self {
            dir: PathBuf::from(dir),
        }
    }

    fn output(&self, args: &[&str]) -> Result<Vec<String>, String> {
        proc::terramate(&self.dir, args).map(|o| proc::lines(&o))
    }
}

fn with_base<'a>(mut args: Vec<&'a str>, base: &'a str) -> Vec<&'a str> {
    if !base.is_empty() {
        args.push("-B");
        args.push(base);
    }
    args
}

impl Terramate for Real {
    fn changed_stacks(&self, base: &str) -> Result<Vec<String>, String> {
        self.output(&with_base(vec!["list", "--changed"], base))
    }

    fn list(&self) -> Result<Vec<String>, String> {
        self.output(&["list"])
    }

    fn why_changed(&self, base: &str) -> Result<Vec<ChangeReason>, String> {
        Ok(self
            .output(&with_base(vec!["list", "--changed", "--why"], base))?
            .iter()
            .map(|l| parse_why_line(l))
            .collect())
    }

    fn run_graph(&self) -> Result<Vec<Edge>, String> {
        let out = proc::terramate(&self.dir, &["experimental", "run-graph", "-l", "stack.dir"])?;
        Ok(
            tfstackplan_cli::catalog::parse_run_graph(&String::from_utf8_lossy(&out))
                .into_iter()
                .map(|e| Edge {
                    from: e.from,
                    to: e.to,
                })
                .collect(),
        )
    }

    fn script_run(&self, o: &ScriptOpts, env: &[(String, String)]) -> Result<(), String> {
        let status = Command::new("terramate")
            .args(o.args())
            .current_dir(&self.dir)
            .envs(env.iter().map(|(k, v)| (k, v)))
            .stdin(Stdio::inherit())
            .stdout(Stdio::from(std::io::stderr()))
            .stderr(Stdio::from(std::io::stderr()))
            .status()
            .map_err(|e| proc::start_error_in("terramate", &self.dir, &e))?;
        if status.success() {
            Ok(())
        } else {
            Err(proc::exit_text(&status))
        }
    }
}

#[cfg(test)]
mod tests;
