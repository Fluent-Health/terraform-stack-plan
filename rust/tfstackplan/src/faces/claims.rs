//! `claims`: list and release apply-lock claims on the control plane.

use std::collections::BTreeSet;
use std::path::Path;
use std::process::ExitCode;

use tfstackplan_cli::flag::FlagSet;
use tfstackplan_cli::gofmt::quote;

use super::{err, out, parse};
use crate::server::client_for_environment;
use crate::{fsx, proc};

/// Runs `claims <list|release>`.
pub fn run(args: &[String]) -> ExitCode {
    let Some(sub) = args.first() else {
        err("tfstackplan claims: expected a subcommand (list|release)\n");
        return ExitCode::from(2);
    };
    match sub.as_str() {
        "list" => list(&args[1..]),
        "release" => release(&args[1..]),
        other => {
            err(&format!(
                "tfstackplan claims: unknown subcommand {}\n",
                quote(other)
            ));
            ExitCode::from(2)
        }
    }
}

fn list(args: &[String]) -> ExitCode {
    let mut fs = FlagSet::new("claims list");
    fs.string(
        "env",
        "",
        "environment name (optional; defaults to discovering from repository)",
    );
    let p = match parse(&fs, args) {
        Ok(p) => p,
        Err(c) => return c,
    };
    let envs = if p.str("env").is_empty() {
        let found = discover_environments(Path::new("."));
        if found.is_empty() {
            err(
                "tfstackplan claims list: --env is required or environments must be discoverable from the repository layout\n",
            );
            return ExitCode::from(2);
        }
        found
    } else {
        vec![p.str("env").to_string()]
    };

    let mut has_server = false;
    let mut errors = Vec::new();
    let mut results = Vec::new();
    for e in &envs {
        let client = client_for_environment(e);
        if !client.enabled() {
            continue;
        }
        has_server = true;
        // Go decodes expires_at as a time.Time: a malformed one fails the decode.
        let decoded = client.claims_list(e).and_then(|claims| {
            claims
                .into_iter()
                .map(|c| {
                    tfstackplan_cli::claims::format_expiry(&c.expires_at)
                        .map(|exp| (c.stack_path, c.owner_pr, exp))
                        .map_err(|x| format!("post /api/claims/list: decode: {x}"))
                })
                .collect::<Result<Vec<_>, _>>()
        });
        match decoded {
            Ok(claims) => results.push((e, claims)),
            Err(x) => errors.push(format!("{e}: {x}")),
        }
    }
    if !has_server {
        err("tfstackplan claims list: no server configured (TFSTACKPLAN_SERVER unset)\n");
        return ExitCode::FAILURE;
    }
    if !errors.is_empty() {
        for fe in errors {
            err(&format!("tfstackplan claims list error: {fe}\n"));
        }
        return ExitCode::FAILURE;
    }
    let mut text = String::new();
    for (env, claims) in results {
        if claims.is_empty() {
            text.push_str(&format!(
                "No apply-lock claims for environment {}\n",
                quote(env)
            ));
            continue;
        }
        text.push_str(&format!(
            "Apply-lock claims for environment {}:\n",
            quote(env)
        ));
        for (stack, pr, expires) in claims {
            text.push_str(&tfstackplan_cli::claims::line(&stack, pr, &expires));
        }
    }
    out(&text);
    ExitCode::SUCCESS
}

fn release(args: &[String]) -> ExitCode {
    let mut fs = FlagSet::new("claims release");
    fs.string("env", "", "environment name (required)")
        .int("pr", 0, "PR number whose claim(s) to release (required)")
        .string(
            "stack",
            "",
            "stack path to release (omit to release all stacks for the PR)",
        );
    let p = match parse(&fs, args) {
        Ok(p) => p,
        Err(c) => return c,
    };
    let (env, pr, stack) = (p.str("env"), p.int("pr"), p.str("stack"));
    if env.is_empty() || pr == 0 {
        err("tfstackplan claims release: --env and --pr are required\n");
        return ExitCode::from(2);
    }
    let client = client_for_environment(env);
    if !client.enabled() {
        err("tfstackplan claims release: no server configured (TFSTACKPLAN_SERVER unset)\n");
        return ExitCode::FAILURE;
    }
    if let Err(e) = client.claims_release(env, pr, stack) {
        err(&format!("tfstackplan claims release: {e}\n"));
        return ExitCode::FAILURE;
    }
    if stack.is_empty() {
        out(&format!(
            "Released all apply-lock claims: env={env} pr=#{pr}\n"
        ));
    } else {
        out(&format!(
            "Released apply-lock claim: env={env} pr=#{pr} stack={stack}\n"
        ));
    }
    ExitCode::SUCCESS
}

/// Go's `discoverEnvironments`: the directories under `stacks/`, else the
/// environments in `terramate list`'s `stacks/<env>/…` paths, else the
/// environments and names of the config's `server` blocks. Go returns the last
/// two in map order; they are sorted here.
fn discover_environments(dir: &Path) -> Vec<String> {
    if let Ok(entries) = std::fs::read_dir(dir.join("stacks")) {
        let mut envs: Vec<String> = entries
            .filter_map(Result::ok)
            .filter(|e| e.file_type().is_ok_and(|t| t.is_dir()))
            .map(|e| e.file_name().to_string_lossy().into_owned())
            .collect();
        envs.sort();
        if !envs.is_empty() {
            return envs;
        }
    }
    if let Ok(stdout) = proc::terramate(dir, &["list"]) {
        let envs: BTreeSet<String> = proc::lines(&stdout)
            .iter()
            .filter_map(|s| {
                let parts: Vec<&str> = s.split('/').collect();
                (parts.len() > 1 && parts[0] == "stacks").then(|| parts[1].to_string())
            })
            .collect();
        if !envs.is_empty() {
            return envs.into_iter().collect();
        }
    }
    if let Some(cfg) = fsx::discover_config(dir).and_then(|p| fsx::load_config(&p).ok()) {
        let envs: BTreeSet<String> = cfg
            .servers
            .iter()
            .flat_map(|s| [s.environment.clone(), s.name.clone()])
            .filter(|s| !s.is_empty())
            .collect();
        return envs.into_iter().collect();
    }
    Vec::new()
}

#[cfg(test)]
mod tests;
