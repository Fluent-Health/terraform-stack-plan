//! `admin`: operator surgery on the control plane (grants, executions, gates, checks).

use std::process::ExitCode;

use tfstackplan_cli::flag::FlagSet;

use super::{err, out, parse};
use crate::server::client_from_env;

/// Runs `admin <grants|executions|gates|checks> <verb>`.
pub fn run(args: &[String]) -> ExitCode {
    let Some(sub) = args.first() else {
        err("tfstackplan admin: expected a subcommand (grants|executions|gates|checks)\n");
        return ExitCode::from(2);
    };
    let rest = &args[1..];
    match sub.as_str() {
        "grants" => grants_or_gates(rest, "grants", "release"),
        "executions" => executions(rest),
        "gates" => grants_or_gates(rest, "gates", "satisfy"),
        "checks" => checks(rest),
        other => {
            err(&format!(
                "tfstackplan admin: unknown subcommand {}\n",
                tfstackplan_cli::gofmt::quote(other)
            ));
            ExitCode::from(2)
        }
    }
}

/// Runs the call on the env's client, printing `Success` or `error: …`.
fn call(f: impl FnOnce(&tfstackplan_clients::server::Client) -> Result<(), String>) -> ExitCode {
    let client = client_from_env();
    if !client.enabled() {
        err("tfstackplan admin: server unset\n");
        return ExitCode::FAILURE;
    }
    match f(&client) {
        Ok(()) => {
            out("Success\n");
            ExitCode::SUCCESS
        }
        Err(e) => {
            err(&format!("error: {e}\n"));
            ExitCode::FAILURE
        }
    }
}

fn grants_or_gates(args: &[String], group: &str, verb: &str) -> ExitCode {
    if args.first().map(String::as_str) != Some(verb) {
        err(&format!("tfstackplan admin {group}: expected '{verb}'\n"));
        return ExitCode::from(2);
    }
    let mut fs = FlagSet::new(&format!("admin {group} {verb}"));
    fs.int("pr", 0, "PR number (required)")
        .string("env", "", "environment name (required)")
        .string("class", "", "grant class (required)")
        .string("target", "", "grant target (required)")
        .string(
            "reason",
            "admin intervention",
            &format!("reason for {verb}"),
        );
    let p = match parse(&fs, &args[1..]) {
        Ok(p) => p,
        Err(c) => return c,
    };
    let (pr, env, class, target, reason) = (
        p.int("pr"),
        p.str("env"),
        p.str("class"),
        p.str("target"),
        p.str("reason"),
    );
    if pr == 0 || env.is_empty() || class.is_empty() || target.is_empty() {
        err(&format!(
            "tfstackplan admin {group} {verb}: --pr, --env, --class, --target are required\n"
        ));
        return ExitCode::from(2);
    }
    call(|c| {
        if group == "grants" {
            c.admin_grants_release(pr, env, class, target, reason)
        } else {
            c.admin_gates_satisfy(pr, env, class, target, reason)
        }
    })
}

fn executions(args: &[String]) -> ExitCode {
    if args.first().map(String::as_str) != Some("cancel") {
        err("tfstackplan admin executions: expected 'cancel'\n");
        return ExitCode::from(2);
    }
    let mut fs = FlagSet::new("admin executions cancel");
    fs.string("id", "", "execution ID (required)").string(
        "reason",
        "admin intervention",
        "reason for cancellation",
    );
    let p = match parse(&fs, &args[1..]) {
        Ok(p) => p,
        Err(c) => return c,
    };
    if p.str("id").is_empty() {
        err("tfstackplan admin executions cancel: --id is required\n");
        return ExitCode::from(2);
    }
    call(|c| c.admin_executions_cancel(p.str("id"), p.str("reason")))
}

fn checks(args: &[String]) -> ExitCode {
    if args.first().map(String::as_str) != Some("override") {
        err("tfstackplan admin checks: expected 'override'\n");
        return ExitCode::from(2);
    }
    let mut fs = FlagSet::new("admin checks override");
    fs.int("pr", 0, "PR number (required)")
        .string("env", "", "environment name (required)")
        .string("check", "", "check name (required)")
        .string("conclusion", "", "override conclusion (required)")
        .string("reason", "admin intervention", "reason for override");
    let p = match parse(&fs, &args[1..]) {
        Ok(p) => p,
        Err(c) => return c,
    };
    let (pr, env, check, conclusion) = (
        p.int("pr"),
        p.str("env"),
        p.str("check"),
        p.str("conclusion"),
    );
    if pr == 0 || env.is_empty() || check.is_empty() || conclusion.is_empty() {
        err("tfstackplan admin checks override: --pr, --env, --check, --conclusion are required\n");
        return ExitCode::from(2);
    }
    call(|c| c.admin_checks_override(pr, env, check, conclusion, p.str("reason")))
}
