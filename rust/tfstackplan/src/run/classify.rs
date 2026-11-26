//! The classify pass shared by `run plan` and `run apply` (Go's
//! `classifyForGate`/`renderClassification`): gather each stack's `tfplan.json`
//! into a temporary plans dir, reconcile pending cross-state moves, render and
//! classify in process with render-core, and read the gates, moving stacks,
//! categories and counts back from the classification sidecar.

use std::collections::BTreeMap;

use tfstackplan_cli::run::{self as core, Category, Counts, GateTarget};
use tfstackplan_cli::statemove::manifest::manifest_json;

use super::terramate::{ScriptOpts, Terramate};
use crate::faces::err;
use crate::faces::state::{collect, walk};
use crate::{gofs, render};

/// Everything a classify pass yields.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct ClassifyResult {
    /// Approvals needed.
    pub gates: Vec<GateTarget>,
    /// Each stack's categories.
    pub categories: BTreeMap<String, Vec<Category>>,
    /// Stacks adopting resources through a cross-state move.
    pub moving: Vec<String>,
    /// Each stack's counts.
    pub counts: BTreeMap<String, Counts>,
    /// The full report (`run plan` prints it).
    pub report: String,
    /// The report without the summary table (what is finalized).
    pub report_no_table: String,
    /// Each stack's plan section.
    pub stack_reports: BTreeMap<String, String>,
}

/// Go's `gatingClasses`: the class names with a `class` binding in the config
/// (`cfg_path`, else discovered under `dir`); none when it cannot be read.
pub fn gating_classes(cfg_path: &str, dir: &str) -> Vec<String> {
    let path = if cfg_path.is_empty() {
        render::discover(dir).unwrap_or_default()
    } else {
        cfg_path.to_string()
    };
    if path.is_empty() {
        return Vec::new();
    }
    gofs::read_file(&path)
        .ok()
        .and_then(|t| tfstackplan_render::config::load(&path, &String::from_utf8_lossy(&t)).ok())
        .map(|c| c.classes)
        .unwrap_or_default()
}

/// Go's `gatherPlans`: copies each stack's `<root>/<stack>/tfplan.json` into a
/// fresh temporary plans dir (`<plans>/<stack>/tfplan.json`). Stacks without a
/// plan are skipped. The directory is removed when dropped.
pub fn gather_plans(root: &str, stacks: &[String]) -> Result<tempfile::TempDir, String> {
    let plans = tempfile::Builder::new()
        .prefix("tfstackplan-plans-")
        .tempdir()
        .map_err(|e| format!("mkdirtemp: {e}"))?;
    for s in stacks {
        let Ok(data) = std::fs::read(gofs::join(&[root, s, "tfplan.json"])) else {
            continue;
        };
        let dst = plans.path().join(s);
        std::fs::create_dir_all(&dst)
            .map_err(|e| gofs::path_error("mkdir", &dst.to_string_lossy(), &e))?;
        let file = dst.join("tfplan.json");
        std::fs::write(&file, data)
            .map_err(|e| gofs::path_error("open", &file.to_string_lossy(), &e))?;
    }
    Ok(plans)
}

/// Go's `validateXMoveManifest`: prints each manifest's diagnostics to stderr
/// and fails on any error.
pub fn validate_xmove_manifest(dir: &str, plans_dir: &str) -> Result<(), String> {
    let xmoves = walk::discover_xmoves(dir)?;
    let plan = |stack: &str| std::fs::read(gofs::join(&[plans_dir, stack, "tfplan.json"])).ok();
    let exists = |rel: &str| std::fs::metadata(gofs::join(&[dir, rel])).is_ok();
    let tf_files = |stack: &str| walk::tf_files(&gofs::join(&[dir, stack]));
    let checked = core::classify::validate_xmoves(
        &xmoves,
        &core::classify::Inputs {
            plan: &plan,
            exists: &exists,
            tf_files: &tf_files,
        },
    );
    err(&checked.stderr);
    if checked.failed {
        return Err("xmove manifest validation failed".to_string());
    }
    Ok(())
}

/// Go's `writeStateMovesManifest`: the two-sided `--state-moves` file for every
/// pending move under `dir`, written into `out_dir`; `None` when nothing is pending.
fn write_state_moves_manifest(dir: &str, out_dir: &str) -> Result<Option<String>, String> {
    let manifest = collect(dir, "")?;
    if manifest.is_empty() {
        return Ok(None);
    }
    validate_xmove_manifest(dir, out_dir)?;
    let path = gofs::join(&[out_dir, "_state_moves.json"]);
    gofs::write_file(&path, manifest_json(&manifest).as_bytes())?;
    Ok(Some(path))
}

/// Go's `renderClassification`: render and classify the plans the stacks' plan
/// scripts already wrote. An empty `cfg_path` discovers `.tfstackplan.hcl` under `dir`.
pub fn render_classification(
    dir: &str,
    stacks: &[String],
    cfg_path: &str,
) -> Result<ClassifyResult, String> {
    let plans = gather_plans(dir, stacks)?;
    let plans_dir = plans.path().to_string_lossy().into_owned();
    let resolved = if cfg_path.is_empty() {
        render::discover(dir).unwrap_or_default()
    } else {
        cfg_path.to_string()
    };
    let sidecar = gofs::join(&[&plans_dir, "_classes.json"]);
    let state_moves = write_state_moves_manifest(dir, &plans_dir)?.unwrap_or_default();
    let o = render::Opts {
        plans_dir: plans_dir.clone(),
        title: "Terraform plan".into(),
        marker: "tfstackplan".into(),
        config: resolved.clone(),
        max_bytes: render::DEFAULT_MAX_BYTES,
        output: "-".into(),
        class_json: sidecar.clone(),
        details: "closed".into(),
        repo_root: dir.to_string(),
        state_moves,
        link_vars: Vec::new(),
    };
    let out = render::render(&o)?;
    let mut res = ClassifyResult {
        report: out.markdown,
        report_no_table: out.markdown_no_table,
        stack_reports: out.stack_reports,
        ..ClassifyResult::default()
    };
    if let Ok(data) = std::fs::read(&sidecar) {
        let view = core::read_sidecar(&data, &gating_classes(&resolved, dir)).unwrap_or_default();
        res.gates = view.gates;
        res.moving = view.moving;
        res.categories = view.categories;
        res.counts = view.counts;
    }
    Ok(res)
}

/// Go's `classifyForGate`: re-runs the `plan` script over the stacks (unless there
/// are none), then renders and classifies. `parallel` mirrors the apply's.
pub fn classify_for_gate(
    tm: &dyn Terramate,
    dir: &str,
    stacks: &[String],
    base: &str,
    changed: bool,
    cfg_path: &str,
    parallel: i64,
) -> Result<ClassifyResult, String> {
    if !stacks.is_empty() {
        tm.script_run(
            &ScriptOpts {
                script: "plan".into(),
                changed,
                parallel,
                base: base.to_string(),
            },
            &[],
        )?;
    }
    render_classification(dir, stacks, cfg_path)
}

#[cfg(test)]
mod tests;
