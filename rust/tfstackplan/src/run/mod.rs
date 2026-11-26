//! `run`: the CI driver. It detects changed stacks with terramate, runs the
//! terramate scripts that plan, apply and verify them, renders and classifies the
//! plans in process, and narrates every step to the control plane. Reporting is
//! best effort except the apply gate, which fails closed; with no server
//! configured every call is a no-op and a local run behaves the same.

use std::path::Path;
use std::process::{Command, ExitCode};

use tfstackplan_cli::flag::FlagSet;
use tfstackplan_cli::gofmt::quote;
use tfstackplan_cli::run::{self as core, Edge, PhaseEvent};
use tfstackplan_clients::server::{Client, Update};

use crate::faces::{code, err, out, parse};
use crate::server::{api_token, client_from_env, env};

pub mod apply;
pub mod cache;
pub mod classify;
pub mod logpump;
pub mod plan;
pub mod terramate;

use terramate::{ScriptOpts, Terramate};

/// Runs `run <verb>`.
pub fn run(args: &[String]) -> ExitCode {
    let Some(verb) = args.first() else {
        err(
            "tfstackplan run: expected a subcommand (tick|phase|wrap|register|exec|lint|plan|apply|verify|claims|status|whoami)\n",
        );
        return ExitCode::from(2);
    };
    let rest = &args[1..];
    let os = Os {
        getenv: &env,
        client: &client_from_env,
        tm: &|dir: &str| -> Box<dyn Terramate> { Box::new(terramate::Real::new(dir)) },
    };
    match verb.as_str() {
        "tick" => code(tick(rest, &os)),
        "phase" => code(phase(rest, &os)),
        "wrap" => crate::faces::wrap::run(rest),
        "register" => code(register(rest, &os)),
        "exec" => code(exec(rest, &os)),
        "lint" => code(lint(rest, &os)),
        "plan" => code(plan::plan(rest, &os)),
        "apply" => code(apply::real_seams(|seams| apply::apply(rest, &os, seams))),
        "verify" => code(verify(rest, &os)),
        "claims" => crate::faces::claims::run(rest),
        "status" => code(status(rest, &os)),
        "whoami" => crate::faces::whoami::run(rest),
        other => {
            err(&format!(
                "tfstackplan run: unknown subcommand {}\n",
                quote(other)
            ));
            ExitCode::from(2)
        }
    }
}

/// What the verbs take from the process: the environment, the control-plane
/// client and the terramate driver. Tests substitute all three.
pub struct Os<'a> {
    /// Reads a variable (empty when unset).
    pub getenv: &'a dyn Fn(&str) -> String,
    /// Builds `runner.ClientFromEnv()`. Called where Go builds it, since building
    /// may print the audience warning.
    pub client: &'a dyn Fn() -> Client,
    /// The terramate driver for a project root.
    pub tm: &'a dyn Fn(&str) -> Box<dyn Terramate>,
}

/// The run context every verb reads from the environment.
pub struct Ctx {
    /// `owner/name`.
    pub repo: String,
    /// Head commit.
    pub sha: String,
    /// Pull request number (0 when unset or not a number).
    pub pr: i64,
    /// Environment.
    pub environment: String,
}

impl Ctx {
    /// `TFSTACKPLAN_REPO`, `_SHA`, `_PR`, `_ENVIRONMENT`.
    pub fn from_env(getenv: &dyn Fn(&str) -> String) -> Self {
        Self {
            repo: getenv("TFSTACKPLAN_REPO"),
            sha: getenv("TFSTACKPLAN_SHA"),
            pr: core::atoi_or_0(&getenv("TFSTACKPLAN_PR")),
            environment: getenv("TFSTACKPLAN_ENVIRONMENT"),
        }
    }

    /// A phase event carrying the run context.
    pub fn phase(&self, id: &str, phase: &str) -> PhaseEvent {
        PhaseEvent {
            id: id.to_string(),
            repo: self.repo.clone(),
            sha: self.sha.clone(),
            pr: self.pr,
            environment: self.environment.clone(),
            phase: phase.to_string(),
            ..PhaseEvent::default()
        }
    }

    /// The Init for these stacks.
    pub fn init(&self, id: &str, context: &str, stacks: &[String], edges: Vec<Edge>) -> core::Init {
        core::Init {
            id: id.to_string(),
            repo: self.repo.clone(),
            sha: self.sha.clone(),
            pr: self.pr,
            environment: self.environment.clone(),
            log_url: String::new(),
            context: context.to_string(),
            stacks: core::pending(stacks),
            edges,
        }
    }
}

/// A bare phase event (`events.PhaseEvent{ID, Phase}`), with an optional label and progress.
pub fn bare_phase(id: &str, phase: &str, label: &str, pct: Option<i64>) -> PhaseEvent {
    PhaseEvent {
        id: id.to_string(),
        phase: phase.to_string(),
        label: label.to_string(),
        progress_pct: pct,
        ..PhaseEvent::default()
    }
}

/// The changed (or all) stacks and their normalized dependency edges.
pub fn stacks_and_edges(
    tm: &dyn Terramate,
    changed: bool,
    base: &str,
) -> Result<(Vec<String>, Vec<Edge>), String> {
    let stacks = if changed {
        tm.changed_stacks(base)?
    } else {
        tm.list()?
    };
    // Best effort: edges only enrich the graph.
    let raw = tm.run_graph().unwrap_or_default();
    let edges = core::normalize_edges(&stacks, &raw);
    Ok((stacks, edges))
}

/// The log pump for a run, when a server is configured and a log file named.
pub fn pump<'a>(
    client: &'a Client,
    dir: &'a str,
    log_file: &'a str,
    exec_id: &'a str,
) -> Option<logpump::Pump<'a>> {
    (client.enabled() && !log_file.is_empty())
        .then(|| logpump::Pump::new(client, dir, log_file, exec_id))
}

/// Go's `time.Now().UnixNano()` for un-orchestrated execution ids.
pub fn unix_nanos() -> u128 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_nanos())
        .unwrap_or_default()
}

/// `run tick`: one stack status, best effort; exits 0 except on a flag error.
fn tick(args: &[String], os: &Os<'_>) -> i32 {
    let mut fs = FlagSet::new("run tick");
    fs.string("stack", "", "stack path (defaults to $TFSTACKPLAN_STACK)")
        .string("status", "", "stack status (e.g. running, planned, failed)")
        .string("detail", "", "optional failure detail");
    let Ok(p) = parse(&fs, args) else { return 2 };
    let mut stack = p.str("stack").to_string();
    if stack.is_empty() {
        stack = (os.getenv)("TFSTACKPLAN_STACK");
    }
    let exec_id = (os.getenv)("TFSTACKPLAN_EXECUTION");
    if p.str("status").is_empty() || stack.is_empty() || exec_id.is_empty() {
        return 0;
    }
    let _ = (os.client)().update(&Update {
        id: exec_id,
        stack,
        status: p.str("status").to_string(),
        detail: p.str("detail").to_string(),
    });
    0
}

/// `run phase`: narrates a lifecycle phase, best effort.
fn phase(args: &[String], os: &Os<'_>) -> i32 {
    let mut fs = FlagSet::new("run phase");
    fs.string("phase", "", "lifecycle phase name")
        .string("label", "", "optional human-readable progress label")
        .int("progress-pct", -1, "optional progress percentage (0-100)");
    let Ok(p) = parse(&fs, args) else { return 2 };
    let exec_id = (os.getenv)("TFSTACKPLAN_EXECUTION");
    if p.str("phase").is_empty() || exec_id.is_empty() {
        return 0;
    }
    let pct = p.int("progress-pct");
    let mut ev = Ctx::from_env(os.getenv).phase(&exec_id, p.str("phase"));
    ev.label = p.str("label").to_string();
    ev.progress_pct = (0..=100).contains(&pct).then_some(pct);
    let _ = (os.client)().phase(&ev);
    0
}

/// `run register`: one Init with every stack pending, so early phase events show
/// the real stack count. A no-op offline.
fn register(args: &[String], os: &Os<'_>) -> i32 {
    let mut fs = FlagSet::new("run register");
    fs.string("dir", ".", "terramate root directory")
        .bool(
            "changed",
            false,
            "only changed stacks (terramate --changed)",
        )
        .string("base", "", "git base ref for --changed");
    let Ok(p) = parse(&fs, args) else { return 2 };
    let client = (os.client)();
    let exec_id = (os.getenv)("TFSTACKPLAN_EXECUTION");
    if !client.enabled() || exec_id.is_empty() {
        return 0;
    }
    let tm = (os.tm)(p.str("dir"));
    let (stacks, edges) = match stacks_and_edges(tm.as_ref(), p.bool("changed"), p.str("base")) {
        Ok(x) => x,
        Err(e) => {
            err(&format!("tfstackplan run register: {e}\n"));
            return 1;
        }
    };
    let _ = client.init(&Ctx::from_env(os.getenv).init(&exec_id, "", &stacks, edges));
    0
}

/// `run exec`: runs one command, ticking a phase first and finalizing failed
/// when it fails. A plain passthrough with no server.
fn exec(args: &[String], os: &Os<'_>) -> i32 {
    let mut fs = FlagSet::new("run exec");
    fs.string(
        "phase",
        "",
        "lifecycle phase to tick before running (warming|linting|initializing|planning|applying|testing|verifying)",
    );
    let Ok(p) = parse(&fs, args) else { return 2 };
    if p.rest.is_empty() {
        err("tfstackplan run exec: command is required (use -- <cmd> [args...])\n");
        return 2;
    }
    let exec_id = (os.getenv)("TFSTACKPLAN_EXECUTION");
    let client = (os.client)();
    if !p.str("phase").is_empty() && !exec_id.is_empty() {
        let _ = client.phase(&Ctx::from_env(os.getenv).phase(&exec_id, p.str("phase")));
    }
    let status = Command::new(&p.rest[0]).args(&p.rest[1..]).status();
    let exit = match status {
        Ok(s) if s.success() => return 0,
        Ok(s) => s.code().unwrap_or(-1),
        Err(_) => 1,
    };
    if !exec_id.is_empty() {
        let _ = client.finalize(&core::Finalize {
            id: exec_id,
            failed: true,
            ..core::Finalize::default()
        });
    }
    exit
}

/// `run lint`: the terramate lint script over the stacks.
fn lint(args: &[String], os: &Os<'_>) -> i32 {
    let mut fs = FlagSet::new("run lint");
    fs.string("dir", "", "terramate project root (required)")
        .bool("changed", true, "only lint changed stacks")
        .int("parallel", 0, "parallel lint jobs (0 = terramate default)")
        .string("base", "", "git base ref for change detection")
        .string("script", "lint", "terramate script name to run");
    let Ok(p) = parse(&fs, args) else { return 2 };
    if p.str("dir").is_empty() {
        err("tfstackplan run lint: --dir is required\n");
        return 2;
    }
    let tm = (os.tm)(p.str("dir"));
    let client = (os.client)();
    let mut exec_id = (os.getenv)("TFSTACKPLAN_EXECUTION");
    if exec_id.is_empty() {
        exec_id = format!("plan-{}", unix_nanos());
    }
    let (stacks, edges) = match stacks_and_edges(tm.as_ref(), p.bool("changed"), p.str("base")) {
        Ok(x) => x,
        Err(e) => {
            err(&format!("tfstackplan run lint: {e}\n"));
            return 1;
        }
    };
    let ctx = Ctx::from_env(os.getenv);
    let _ = client.init(&ctx.init(&exec_id, "", &stacks, edges));
    let _ = client.phase(&bare_phase(&exec_id, "linting", "", None));
    let mut result = Ok(());
    if !stacks.is_empty() {
        result = tm.script_run(
            &ScriptOpts {
                script: p.str("script").to_string(),
                changed: p.bool("changed"),
                parallel: p.int("parallel"),
                base: p.str("base").to_string(),
            },
            &[("TFSTACKPLAN_EXECUTION".to_string(), exec_id.clone())],
        );
    }
    if let Err(e) = result {
        err(&format!("tfstackplan run lint: lint failed: {e}\n"));
        let _ = client.finalize(&core::Finalize {
            id: exec_id,
            failed: true,
            ..core::Finalize::default()
        });
        return 1;
    }
    0
}

/// `run verify`: the terramate verify script, reported as its own check run.
/// It never gates.
fn verify(args: &[String], os: &Os<'_>) -> i32 {
    let mut fs = FlagSet::new("run verify");
    fs.string("dir", "", "terramate project root (required)")
        .bool("changed", true, "only verify changed stacks")
        .string("base", "", "git base ref for change detection")
        .string("script", "verify", "terramate script name to run")
        .string(
            "log-file",
            "tfstackplan.log",
            "per-stack log filename the terramate script writes in each stack dir; streamed live to the server (empty disables)",
        );
    let Ok(p) = parse(&fs, args) else { return 2 };
    let dir = p.str("dir");
    if dir.is_empty() {
        err("tfstackplan run verify: --dir is required\n");
        return 2;
    }
    let client = (os.client)();
    let tm = (os.tm)(dir);
    let ctx = Ctx::from_env(os.getenv);
    let mut exec_id = (os.getenv)("TFSTACKPLAN_EXECUTION");
    if exec_id.is_empty() {
        exec_id = format!("verify-{}", unix_nanos());
    }
    let (stacks, edges) = match stacks_and_edges(tm.as_ref(), p.bool("changed"), p.str("base")) {
        Ok(x) => x,
        Err(e) => {
            err(&format!("tfstackplan run verify: {e}\n"));
            return 1;
        }
    };
    let context = if ctx.environment.is_empty() {
        "verify".to_string()
    } else {
        format!("verify/{}", ctx.environment)
    };
    let _ = client.init(&ctx.init(&exec_id, &context, &stacks, edges));
    let _ = client.phase(&bare_phase(&exec_id, "verifying", "", None));
    let mut result = Ok(());
    if !stacks.is_empty() {
        result = logpump::while_pumping(
            pump(&client, dir, p.str("log-file"), &exec_id),
            &stacks,
            logpump::INTERVAL,
            || {
                tm.script_run(
                    &ScriptOpts {
                        script: p.str("script").to_string(),
                        changed: p.bool("changed"),
                        parallel: 0,
                        base: p.str("base").to_string(),
                    },
                    &[("TFSTACKPLAN_EXECUTION".to_string(), exec_id.clone())],
                )
            },
        );
    }
    let report = match &result {
        Ok(()) => format!("✅ Verification passed ({} stacks).", stacks.len()),
        Err(_) => "❌ Verification failed.".to_string(),
    };
    let _ = client.finalize(&core::Finalize {
        id: exec_id,
        report_markdown: report,
        failed: result.is_err(),
        ..core::Finalize::default()
    });
    if let Err(e) = result {
        err(&format!("tfstackplan run verify: verify failed: {e}\n"));
        return 1;
    }
    0
}

/// `run status`: prints an execution, optionally watching it to its conclusion.
fn status(args: &[String], os: &Os<'_>) -> i32 {
    let mut fs = FlagSet::new("run status");
    fs.string(
        "server",
        "",
        "server base URL (defaults to $TFSTACKPLAN_SERVER)",
    )
    .string("format", "text", "output format: text|json")
    .bool(
        "watch",
        false,
        "block and watch for real-time status updates",
    );
    let Ok(p) = parse(&fs, args) else { return 2 };
    let mut exec_id = p.rest.first().cloned().unwrap_or_default();
    if exec_id.is_empty() {
        exec_id = (os.getenv)("TFSTACKPLAN_EXECUTION");
    }
    if exec_id.is_empty() {
        err("run status: execution ID is required as an argument or via $TFSTACKPLAN_EXECUTION\n");
        return 2;
    }
    let mut srv = p.str("server").to_string();
    if srv.is_empty() {
        srv = (os.getenv)("TFSTACKPLAN_SERVER");
    }
    if srv.is_empty()
        && let Some(cfg) = crate::fsx::discover_config(Path::new("."))
            .and_then(|c| crate::fsx::load_config(&c).ok())
    {
        let environment = (os.getenv)("TFSTACKPLAN_ENVIRONMENT");
        let matched = (!environment.is_empty())
            .then(|| {
                cfg.servers
                    .iter()
                    .find(|s| s.environment == environment || s.name == environment)
                    .map(|s| s.url.clone())
            })
            .flatten()
            .filter(|u| !u.is_empty());
        srv = matched
            .or_else(|| cfg.server.as_ref().map(|s| s.url.clone()))
            .unwrap_or_default();
    }
    if srv.is_empty() {
        err("run status: server URL is required\n");
        return 2;
    }
    let srv = srv.trim_end_matches('/').to_string();
    let aud_env = (os.getenv)("TFSTACKPLAN_AUDIENCE");
    let aud = if aud_env.is_empty() {
        srv.clone()
    } else {
        aud_env.clone()
    };
    let token = match api_token(&aud) {
        Ok(t) => t,
        Err(e) => {
            if !aud_env.is_empty() {
                err(&format!(
                    "run status: OIDC audience is set but Google ADC is unavailable ({e}) — requests will be unauthenticated\n"
                ));
            }
            None
        }
    };
    let api = match tfstackplan_clients::status::StatusClient::new(&srv, token) {
        Ok(a) => a,
        Err(e) => {
            err(&format!("run status: fetch failed: {e}\n"));
            return 1;
        }
    };
    let fetch = || {
        api.execution(&exec_id)
            .and_then(|b| core::status::decode(&b))
    };
    let format = p.str("format");
    let print = |e: &core::status::Execution, clear: bool| {
        if format == "json" {
            match core::status::json(e) {
                Ok(s) => out(&s),
                Err(x) => err(&format!("{x}\n")),
            }
            return;
        }
        if clear && std::io::IsTerminal::is_terminal(&std::io::stdout()) {
            out("\x1b[H\x1b[2J");
        }
        out(&core::status::text(e));
    };
    let first = match fetch() {
        Ok(e) => e,
        Err(e) => {
            err(&format!("run status: fetch failed: {e}\n"));
            return 1;
        }
    };
    print(&first, false);
    if !p.bool("watch") || first.terminal() {
        return first.exit_code();
    }
    let mut last: Option<core::status::Execution> = None;
    let streamed = api.events(&exec_id, |line| {
        if line.starts_with("data: changed")
            && let Ok(e) = fetch()
        {
            print(&e, true);
            if e.terminal() {
                last = Some(e);
                return false;
            }
        }
        true
    });
    if let Some(e) = last {
        return e.exit_code();
    }
    if let Err(e) = streamed {
        err(&format!("run status watch: {e}\n"));
        return 1;
    }
    match fetch() {
        Ok(e) if e.terminal() => e.exit_code(),
        Ok(e) => {
            err(&format!(
                "run status watch: events stream ended prematurely while execution was {}\n",
                e.status()
            ));
            1
        }
        Err(e) => {
            err(&format!("run status watch: {e}\n"));
            1
        }
    }
}

#[cfg(test)]
mod tests;
