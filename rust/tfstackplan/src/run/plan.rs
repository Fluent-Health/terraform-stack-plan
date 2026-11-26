//! `run plan`: detect changed stacks, register the execution, warm the provider
//! cache, run the terramate plan script with the live log pump, then render and
//! classify the plans and finalize them to the server. Reporting is best effort;
//! the report is always printed, so an offline run is still useful.

use std::path::PathBuf;
use std::sync::Arc;

use tfstackplan_cli::flag::FlagSet;
use tfstackplan_cli::run::Finalize;
use tfstackplan_clients::server::Client;

use super::cache::ProviderCache;
use super::classify::render_classification;
use super::terramate::ScriptOpts;
use super::{Ctx, Os, bare_phase, logpump, pump, stacks_and_edges, unix_nanos};
use crate::faces::{err, out, parse};
use crate::{gofs, render};

/// What finalizes an execution if the run leaves before its own terminal Finalize
/// (Go's deferred abort Finalize).
pub struct AbortGuard<'a> {
    client: &'a Client,
    id: String,
    report: &'static str,
    /// Set once the run sent its own terminal Finalize.
    pub finalized: bool,
}

impl<'a> AbortGuard<'a> {
    /// A guard that reports `report` unless [`AbortGuard::finalized`] is set.
    pub fn new(client: &'a Client, id: &str, report: &'static str) -> Self {
        Self {
            client,
            id: id.to_string(),
            report,
            finalized: false,
        }
    }
}

impl Drop for AbortGuard<'_> {
    fn drop(&mut self) {
        if !self.finalized && self.client.enabled() {
            let _ = self.client.finalize(&Finalize {
                id: self.id.clone(),
                failed: true,
                report_markdown: self.report.to_string(),
                ..Finalize::default()
            });
        }
    }
}

/// The `cache {}` settings with Go's defaults filled in.
pub struct CacheSettings {
    /// Bucket (`$TFSTACKPLAN_CACHE_BUCKET` when unset).
    pub bucket: String,
    /// Prefix (`infra/tf-plugins` when unset).
    pub prefix: String,
    /// Version (`$TFSTACKPLAN_CACHE_VERSION`, else `0`, when unset).
    pub version: String,
}

/// Loads the config for the cache block (`cfg_path`, else discovered under
/// `dir`). A load error is reported as `<who>: cache config: <err>` and ignored.
pub fn cache_settings(
    who: &str,
    cfg_path: &str,
    dir: &str,
    getenv: &dyn Fn(&str) -> String,
) -> Option<CacheSettings> {
    let path = if cfg_path.is_empty() {
        render::discover(dir).unwrap_or_default()
    } else {
        cfg_path.to_string()
    };
    if path.is_empty() {
        return None;
    }
    let loaded = gofs::read_file(&path)
        .and_then(|t| tfstackplan_render::config::load(&path, &String::from_utf8_lossy(&t)));
    let cfg = match loaded {
        Ok(c) => c,
        Err(e) => {
            err(&format!("{who}: cache config: {e}\n"));
            return None;
        }
    };
    let c = cfg.cache?;
    let or = |v: String, d: String| if v.is_empty() { d } else { v };
    let version = or(c.version, getenv("TFSTACKPLAN_CACHE_VERSION"));
    Some(CacheSettings {
        bucket: or(c.bucket, getenv("TFSTACKPLAN_CACHE_BUCKET")),
        prefix: or(c.prefix, "infra/tf-plugins".to_string()),
        version: or(version, "0".to_string()),
    })
}

/// Warms the provider cache for the stacks (Go's warm block in plan and apply):
/// emits the `warming` phase, builds the GCS store from ADC and restores every
/// pinned provider. Failures are warnings worded `<who>: …`.
pub fn warm_cache(
    who: &str,
    settings: &CacheSettings,
    client: &Client,
    exec_id: &str,
    dir: &str,
    stacks: &[String],
) -> Option<ProviderCache> {
    let _ = client.phase(&bare_phase(exec_id, "warming", "", None));
    let token = match tfstackplan_clients::google::access_token_source() {
        Ok(t) => t,
        Err(e) => {
            err(&format!("{who}: cache warming: no GCP credentials: {e}\n"));
            return None;
        }
    };
    let store = match tfstackplan_clients::cache::GcsStorage::new(token, &settings.bucket, &settings.prefix)
    {
        Ok(s) => s,
        Err(e) => {
            err(&format!("{who}: warming cache warning: {e}\n"));
            return None;
        }
    };
    let pc = match ProviderCache::new(Arc::new(store), "", &settings.version) {
        Ok(p) => p,
        Err(e) => {
            err(&format!("{who}: warming cache warning: {e}\n"));
            return None;
        }
    };
    let paths: Vec<PathBuf> = stacks.iter().map(|s| PathBuf::from(gofs::join(&[dir, s]))).collect();
    if let Err(e) = pc.warm(&paths) {
        err(&format!("{who}: warming cache warning: {e}\n"));
    }
    Some(pc)
}

/// Runs `run plan`.
pub fn plan(args: &[String], os: &Os<'_>) -> i32 {
    let mut fs = FlagSet::new("run plan");
    fs.string("dir", "", "terramate project root (required)")
        .bool("changed", true, "only plan changed stacks")
        .int("parallel", 0, "parallel plan jobs (0 = terramate default)")
        .string("base", "", "git base ref for change detection")
        .string("script", "plan", "terramate script name to run")
        .string(
            "log-file",
            "tfstackplan.log",
            "per-stack log filename the terramate script writes in each stack dir; streamed live to the server (empty disables)",
        )
        .string(
            "config",
            "",
            "HCL config (default: auto-discover .tfstackplan.hcl under --dir)",
        );
    let Ok(p) = parse(&fs, args) else { return 2 };
    let dir = p.str("dir");
    if dir.is_empty() {
        err("tfstackplan run plan: --dir is required\n");
        return 2;
    }
    let (changed, base, cfg_path) = (p.bool("changed"), p.str("base"), p.str("config"));
    let tm = (os.tm)(dir);
    let client = (os.client)();
    let mut exec_id = (os.getenv)("TFSTACKPLAN_EXECUTION");
    if exec_id.is_empty() {
        exec_id = format!("plan-{}", unix_nanos());
    }
    let (stacks, edges) = match stacks_and_edges(tm.as_ref(), changed, base) {
        Ok(x) => x,
        Err(e) => {
            err(&format!("tfstackplan run plan: {e}\n"));
            return 1;
        }
    };
    let ctx = Ctx::from_env(os.getenv);
    let _ = client.init(&ctx.init(&exec_id, "", &stacks, edges));
    let mut guard = AbortGuard::new(
        &client,
        &exec_id,
        "tfstackplan run plan: run aborted prematurely or failed during pre-flight validation.",
    );

    let pc = cache_settings("tfstackplan run plan", cfg_path, dir, os.getenv)
        .and_then(|c| warm_cache("tfstackplan run plan", &c, &client, &exec_id, dir, &stacks));

    let _ = client.phase(&bare_phase(&exec_id, "planning", "", None));
    let mut script = Ok(());
    if !stacks.is_empty() {
        script = logpump::while_pumping(
            pump(&client, dir, p.str("log-file"), &exec_id),
            &stacks,
            logpump::INTERVAL,
            || {
                tm.script_run(
                    &ScriptOpts {
                        script: p.str("script").to_string(),
                        changed,
                        parallel: p.int("parallel"),
                        base: base.to_string(),
                    },
                    &[("TFSTACKPLAN_EXECUTION".to_string(), exec_id.clone())],
                )
            },
        );
    }
    if let Some(pc) = &pc
        && let Err(e) = pc.save()
    {
        err(&format!("tfstackplan run plan: cache save: {e}\n"));
    }

    let _ = client.phase(&bare_phase(&exec_id, "classify", "classifying plans…", Some(95)));
    let res = match render_classification(dir, &stacks, cfg_path) {
        Ok(r) => r,
        Err(e) => {
            err(&format!("tfstackplan run plan: {e}\n"));
            return 1;
        }
    };
    out(&res.report);
    let _ = client.phase(&bare_phase(&exec_id, "report", "generating report…", Some(98)));

    let change_reasons = if changed && client.enabled() {
        tm.why_changed(base).unwrap_or_default()
    } else {
        Vec::new()
    };
    let _ = client.finalize(&Finalize {
        id: exec_id.clone(),
        report_markdown: res.report_no_table,
        stack_reports: res.stack_reports,
        gates: res.gates,
        moving: res.moving,
        failed: script.is_err(),
        categories: res.categories,
        counts: res.counts,
        change_reasons,
    });
    guard.finalized = true;
    if let Err(e) = script {
        err(&format!("tfstackplan run plan: plan failed: {e}\n"));
        return 1;
    }
    0
}

#[cfg(test)]
mod tests;
