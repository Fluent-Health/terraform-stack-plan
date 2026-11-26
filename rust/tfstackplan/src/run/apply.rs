//! `run apply`: the CI apply driver. It is self-sufficient (it re-classifies and
//! re-requests grants at apply time) and self-explaining (every exit path sends a
//! terminal Finalize). Order:
//!
//! 1. parse flags; compute the changed stacks.
//! 2. zero stacks: Init(empty) + Finalize(success) + exit 0, never touching the gate.
//! 3. Init(apply context); warm the provider cache.
//! 4. classify pass, then Finalize{gates, …}. Under `--impersonate-requester` a
//!    classify failure aborts (fail closed).
//! 5. gate check (fail closed): on refusal, guidance on stderr, exit 1.
//! 6. optionally mint a token for the leased requester (into the env of the move
//!    pre-phase's terraform and of the scripts).
//! 7. cross-state move pre-phase: on failure Finalize{failed} + exit 1.
//! 8. the terramate apply script, with the log pump.
//! 9. terminal Finalize{failed: apply failed}.
//! 10. best-effort gate revoke; exit 0/1.

use tfstackplan_cli::flag::FlagSet;
use tfstackplan_cli::run::{self as core, Finalize, GateKey};
use tfstackplan_clients::server::LogChunk;

use super::classify::{ClassifyResult, classify_for_gate};
use super::plan::{AbortGuard, cache_settings, warm_cache};
use super::terramate::{ScriptOpts, Terramate};
use super::{Ctx, Os, bare_phase, logpump, pump, stacks_and_edges, unix_nanos};
use crate::faces::err;
use crate::faces::parse;
use crate::faces::state::{StateLock, apply_moves, state_lock};

/// The classify pass (Go's `classifyForGateFn`): terramate, dir, stacks, base,
/// changed, config, parallel.
pub type ClassifyFn<'a> = &'a dyn Fn(
    &dyn Terramate,
    &str,
    &[String],
    &str,
    bool,
    &str,
    i64,
) -> Result<ClassifyResult, String>;

/// The cross-state move pre-phase (Go's `applyMovesFn`): dir, the state lock, the
/// extra terraform env (the impersonation token, which Go `os.Setenv`s), and a
/// sink for each move line with its destination stack.
pub type MovesFn<'a> = &'a dyn Fn(
    &str,
    Option<&StateLock>,
    &[(String, String)],
    &mut dyn FnMut(&str, &str),
) -> Result<(), String>;

/// The seams Go's apply tests replace.
pub struct Seams<'a> {
    /// The classify pass.
    pub classify: ClassifyFn<'a>,
    /// The move pre-phase.
    pub moves: MovesFn<'a>,
    /// Mints an access token for a service account (Go's `mintAccessToken`).
    pub mint: &'a dyn Fn(&str) -> Result<String, String>,
    /// Builds the GCS state lock (`--state-lock`).
    pub lock: &'a dyn Fn() -> Result<StateLock, String>,
}

/// The real seams.
pub fn real_seams<R>(f: impl FnOnce(&Seams<'_>) -> R) -> R {
    let moves = |dir: &str,
                 lock: Option<&StateLock>,
                 env: &[(String, String)],
                 sink: &mut dyn FnMut(&str, &str)| {
        apply_moves(dir, true, lock, env, &mut std::io::stderr(), Some(sink))
    };
    f(&Seams {
        classify: &classify_for_gate,
        moves: &moves,
        mint: &tfstackplan_clients::google::impersonated_access_token,
        lock: &state_lock,
    })
}

/// Runs `run apply`.
pub fn apply(args: &[String], os: &Os<'_>, seams: &Seams<'_>) -> i32 {
    let mut fs = FlagSet::new("run apply");
    fs.string("dir", "", "terramate project root (required)")
        .bool("changed", true, "only apply changed stacks")
        .int(
            "parallel",
            0,
            "parallel apply jobs (0 = terramate default, serial); terramate still honors dependency order",
        )
        .string("base", "", "git base ref for change detection")
        .string("script", "apply", "terramate script name to run")
        .string(
            "log-file",
            "tfstackplan.log",
            "per-stack log filename the terramate script writes in each stack dir; streamed live to the server (empty disables)",
        )
        .bool(
            "state-lock",
            false,
            "acquire a pessimistic GCS state lock around cross-state moves (fail-fast; requires ADC)",
        )
        .bool(
            "impersonate-requester",
            false,
            "run apply AS the leased PAM requester SA (mints GOOGLE_OAUTH_ACCESS_TOKEN for it)",
        )
        .string(
            "config",
            "",
            "HCL config for the classify pass (default: auto-discover .tfstackplan.hcl under --dir)",
        );
    let Ok(p) = parse(&fs, args) else { return 2 };
    let dir = p.str("dir");
    if dir.is_empty() {
        err("tfstackplan run apply: --dir is required\n");
        return 2;
    }
    let (changed, base, cfg_path, parallel) = (
        p.bool("changed"),
        p.str("base"),
        p.str("config"),
        p.int("parallel"),
    );
    let impersonate = p.bool("impersonate-requester");
    let client = (os.client)();
    let ctx = Ctx::from_env(os.getenv);
    let tm = (os.tm)(dir);
    let mut exec_id = (os.getenv)("TFSTACKPLAN_EXECUTION");
    if exec_id.is_empty() {
        exec_id = format!("apply-{}", unix_nanos());
    }

    // 1. The changed stacks, before touching the gate.
    let (stacks, edges) = match stacks_and_edges(tm.as_ref(), changed, base) {
        Ok(x) => x,
        Err(e) => {
            err(&format!("tfstackplan run apply: {e}\n"));
            return 1;
        }
    };
    let context = if ctx.environment.is_empty() {
        "apply".to_string()
    } else {
        format!("apply/{}", ctx.environment)
    };
    let _ = client.init(&ctx.init(&exec_id, &context, &stacks, edges));
    let mut guard = AbortGuard::new(
        &client,
        &exec_id,
        "tfstackplan run apply: run aborted prematurely or failed during pre-flight validation.",
    );

    // 2. Nothing to apply: conclude success without consulting the gate.
    if stacks.is_empty() {
        let _ = client.finalize(&Finalize {
            id: exec_id.clone(),
            ..Finalize::default()
        });
        guard.finalized = true;
        err("tfstackplan run apply: no changed stacks — nothing to apply\n");
        return 0;
    }

    // 3. Warm the provider cache before the classify pass's parallel inits.
    let pc = cache_settings("tfstackplan run apply", cfg_path, dir, os.getenv)
        .and_then(|c| warm_cache("tfstackplan run apply", &c, &client, &exec_id, dir, &stacks));

    // 4. Classify pass: re-establish the gate's classification and grant requests.
    match (seams.classify)(tm.as_ref(), dir, &stacks, base, changed, cfg_path, parallel) {
        Err(e) if impersonate => {
            err(&format!(
                "tfstackplan run apply: classify pass failed under --impersonate-requester; refusing to apply unelevated: {e}\n"
            ));
            let _ = client.finalize(&Finalize {
                id: exec_id.clone(),
                failed: true,
                report_markdown: format!(
                    "Classify pass failed before an elevated apply; refusing to proceed under the ambient identity (would 403 on gated resources despite an active grant). Retry this tier's apply — classify flakes are transient — or break-glass per docs/ci-cd.md. Cause: {e}"
                ),
                ..Finalize::default()
            });
            guard.finalized = true;
            return 1;
        }
        Err(e) => {
            err(&format!(
                "tfstackplan run apply: classify pass failed (continuing to gate check): {e}\n"
            ));
        }
        Ok(res) => {
            let summary = core::gate_summary(&res.gates);
            let _ = client.finalize(&Finalize {
                id: exec_id.clone(),
                report_markdown: res.report_no_table,
                gates: res.gates,
                categories: res.categories,
                counts: res.counts,
                moving: res.moving,
                ..Finalize::default()
            });
            err(&format!(
                "tfstackplan run apply: classify gates: {summary}\n"
            ));
        }
    }

    // 5. The fail-closed gate.
    let key = GateKey {
        pr: ctx.pr,
        environment: ctx.environment.clone(),
    };
    let verdict = client.gate_check(&key);
    if verdict.kind != core::VerdictKind::Satisfied {
        err(&core::gate_guidance(
            &verdict,
            &(os.getenv)("TFSTACKPLAN_SERVER"),
            ctx.pr,
        ));
        return 1;
    }

    // 6. Optionally run as the leased requester: the token reaches both the move
    //    pre-phase and the apply script, as Go's os.Setenv does.
    let mut token_env = Vec::new();
    if impersonate && !verdict.requester.is_empty() {
        match (seams.mint)(&verdict.requester) {
            Ok(tok) => {
                token_env.push(("GOOGLE_OAUTH_ACCESS_TOKEN".to_string(), tok));
                err(&format!(
                    "tfstackplan run apply: applying AS {}\n",
                    verdict.requester
                ));
            }
            Err(e) => {
                err(&format!(
                    "tfstackplan run apply: impersonate {} : {e}\n",
                    verdict.requester
                ));
                return 1;
            }
        }
    } else if impersonate {
        err(
            "tfstackplan run apply: gate resolved no leased requester — applying under the ambient identity (no gated targets for this PR/env)\n",
        );
    }

    let _ = client.phase(&bare_phase(
        &exec_id,
        "moves",
        "executing state moves…",
        Some(15),
    ));

    // 7. The cross-state move pre-phase (fail closed).
    let lock = if p.bool("state-lock") {
        match (seams.lock)() {
            Ok(l) => Some(l),
            Err(e) => {
                err(&format!("tfstackplan run apply: --state-lock: {e}\n"));
                return 1;
            }
        }
    } else {
        None
    };
    let mut sink = |stack: &str, line: &str| {
        let _ = client.log_chunk(&LogChunk {
            id: exec_id.clone(),
            stack: stack.to_string(),
            data: format!("{line}\n"),
        });
    };
    if let Err(e) = (seams.moves)(dir, lock.as_ref(), &token_env, &mut sink) {
        err(&format!(
            "tfstackplan run apply: cross-state move failed: {e}\n"
        ));
        let _ = client.finalize(&Finalize {
            id: exec_id.clone(),
            failed: true,
            report_markdown: format!("cross-state move failed: {e}"),
            ..Finalize::default()
        });
        guard.finalized = true;
        let _ = client.gate_revoke(&key);
        return 1;
    }

    // 8. The apply script, in dependency order.
    let mut script_env = vec![("TFSTACKPLAN_EXECUTION".to_string(), exec_id.clone())];
    script_env.extend(token_env);
    let _ = client.phase(&bare_phase(&exec_id, "applying", "", None));
    let applied = logpump::while_pumping(
        pump(&client, dir, p.str("log-file"), &exec_id),
        &stacks,
        logpump::INTERVAL,
        || {
            tm.script_run(
                &ScriptOpts {
                    script: p.str("script").to_string(),
                    changed,
                    parallel,
                    base: base.to_string(),
                },
                &script_env,
            )
        },
    );
    if let Some(pc) = &pc
        && let Err(e) = pc.save()
    {
        err(&format!("tfstackplan run apply: cache save: {e}\n"));
    }
    let _ = client.phase(&bare_phase(
        &exec_id,
        "report",
        "generating report…",
        Some(98),
    ));

    // 9. Always a terminal Finalize.
    let _ = client.finalize(&Finalize {
        id: exec_id.clone(),
        failed: applied.is_err(),
        ..Finalize::default()
    });
    guard.finalized = true;

    // 10. Best-effort grant cleanup.
    let _ = client.gate_revoke(&key);
    if let Err(e) = applied {
        err(&format!("tfstackplan run apply: apply failed: {e}\n"));
        return 1;
    }
    0
}

#[cfg(test)]
mod tests;
