//! The cross-state executor (Go's `statemove.Execute` and `applyPendingMoves`):
//! lock, init, pull, back up, validate, decide, `state mv`, push, and roll the
//! source back if the destination push fails.

use std::io::Write;
use std::path::{Path, PathBuf};

use tfstackplan_cli::statemove::decide::Decision;
use tfstackplan_cli::statemove::manifest::FoundXMove;
use tfstackplan_cli::statemove::{
    AddressSet, Move, Severity, State, XMove, decide, dest_providers, expand_pairs, paths,
    state_addresses, validate_move_plan,
};

use super::walk;
use crate::fsx::os_error;

/// The terraform operations the executor needs (the seam tests fake).
pub trait Runner {
    /// `terraform init`.
    fn init(&mut self) -> Result<(), String>;
    /// `terraform state pull`: the state JSON (empty for a never-applied stack).
    fn state_pull(&mut self) -> Result<String, String>;
    /// `terraform state push [-force] <path>`.
    fn state_push(&mut self, path: &Path, force: bool) -> Result<(), String>;
    /// `terraform show -json <path>`, parsed.
    fn show_state_file(&mut self, path: &Path) -> Result<State, String>;
    /// `terraform state mv -state=<state> -state-out=<state_out> <from> <to>`.
    fn state_mv(
        &mut self,
        from: &str,
        to: &str,
        state: &Path,
        state_out: &Path,
    ) -> Result<(), String>;
}

/// Releases a held lock; the release error is ignored, as in Go.
pub type Release<'a> = Box<dyn FnOnce() + 'a>;

/// A pessimistic lock taken before the pull → mv → push window.
pub trait Locker {
    /// Locks a stack's state; an error means it is held elsewhere.
    fn acquire(&self, stack_dir: &str) -> Result<Release<'_>, String>;
}

/// Builds a [`Runner`] rooted at a stack directory.
pub type NewRunner<'a> = Box<dyn Fn(&str) -> Result<Box<dyn Runner + 'a>, String> + 'a>;

/// What the executor runs with.
pub struct ExecDeps<'a> {
    /// The runner factory.
    pub new_tf: NewRunner<'a>,
    /// Where pre-move state backups go (`None` disables them).
    pub backup_dir: Option<PathBuf>,
    /// The optional pessimistic lock.
    pub locker: Option<&'a dyn Locker>,
}

/// What the executor did, or would do, for one concrete pair.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Action {
    /// Source address.
    pub from: String,
    /// Destination address.
    pub to: String,
    /// Move or skip.
    pub decision: Decision,
}

fn temp_dir(prefix: &str) -> Result<tempfile::TempDir, String> {
    tempfile::Builder::new()
        .prefix(prefix)
        .tempdir()
        .map_err(|e| format!("mkdirtemp {prefix}*: {e}"))
}

fn write_private(path: &Path, data: &str) -> Result<(), String> {
    use std::os::unix::fs::OpenOptionsExt;
    let mut f = std::fs::OpenOptions::new()
        .write(true)
        .create(true)
        .truncate(true)
        .mode(0o600)
        .open(path)
        .map_err(|e| os_error("open", path, &e))?;
    f.write_all(data.as_bytes())
        .map_err(|e| os_error("write", path, &e))
}

fn backup(dir: Option<&Path>, stack: &str, state: &str) -> Result<(), String> {
    let Some(dir) = dir else { return Ok(()) };
    let bd = dir.join(stack);
    std::fs::create_dir_all(&bd).map_err(|e| os_error("mkdir", &bd, &e))?;
    let nanos = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_nanos())
        .unwrap_or_default();
    write_private(&bd.join(format!("{nanos}.tfstate")), state)
}

/// The addresses in a pulled state. A blank state (a never-applied stack) is an
/// empty set and is not written, so `state mv -state-out` creates the file.
fn addresses_of(tf: &mut dyn Runner, state: &str, file: &Path) -> Result<AddressSet, String> {
    if state.trim().is_empty() {
        return Ok(AddressSet::new());
    }
    write_private(file, state)?;
    Ok(state_addresses(&tf.show_state_file(file)?))
}

/// Go's `Execute`: one xmove, destination `root/dest_stack`, source
/// `root/<source_stack>`. Fail-closed: any error aborts before pushing.
pub fn execute(
    deps: &ExecDeps<'_>,
    root: &str,
    dest_stack: &str,
    xm: &XMove,
    dry_run: bool,
) -> Result<Vec<Action>, String> {
    let src_dir = paths::join(&[root, &xm.source_stack]);
    let dst_dir = paths::join(&[root, dest_stack]);

    // Held until this function returns; locals drop in reverse, so the
    // destination lock is released first, as Go's deferred calls run.
    let mut _src_lock = ReleaseGuard(None);
    let mut _dst_lock = ReleaseGuard(None);
    if let Some(l) = deps.locker {
        _src_lock.0 = Some(
            l.acquire(&src_dir)
                .map_err(|e| format!("lock source {}: {e}", xm.source_stack))?,
        );
        _dst_lock.0 = Some(
            l.acquire(&dst_dir)
                .map_err(|e| format!("lock dest {dest_stack}: {e}"))?,
        );
    }

    let mut src_tf = (deps.new_tf)(&src_dir)?;
    let mut dst_tf = (deps.new_tf)(&dst_dir)?;
    src_tf
        .init()
        .map_err(|e| format!("init source {}: {e}", xm.source_stack))?;
    dst_tf
        .init()
        .map_err(|e| format!("init dest {dest_stack}: {e}"))?;
    let src_state = src_tf
        .state_pull()
        .map_err(|e| format!("pull source state: {e}"))?;
    let dst_state = dst_tf
        .state_pull()
        .map_err(|e| format!("pull dest state: {e}"))?;

    let tmp = temp_dir("tfsp-xmove-")?;
    let src_file = tmp.path().join("source.tfstate");
    let dst_file = tmp.path().join("dest.tfstate");
    backup(deps.backup_dir.as_deref(), &xm.source_stack, &src_state)?;
    backup(deps.backup_dir.as_deref(), dest_stack, &dst_state)?;

    let src_addrs = addresses_of(src_tf.as_mut(), &src_state, &src_file)
        .map_err(|e| format!("read source state: {e}"))?;
    let dst_addrs = addresses_of(dst_tf.as_mut(), &dst_state, &dst_file)
        .map_err(|e| format!("read dest state: {e}"))?;

    let providers = dest_providers(walk::tf_files(&dst_dir).iter().map(String::as_str));
    let diags = validate_move_plan(
        &src_addrs,
        &dst_addrs,
        &AddressSet::new(),
        &providers,
        xm,
        true,
    );
    if let Some(d) = diags.iter().find(|d| d.severity == Severity::Error) {
        return Err(format!("validation error: {}", d.message));
    }

    let mut actions = Vec::new();
    let mut to_move: Vec<Move> = Vec::new();
    for p in expand_pairs(&src_addrs, &dst_addrs, &xm.pairs) {
        let d = decide(&src_addrs, &dst_addrs, &p.from, &p.to)
            .map_err(|e| format!("move {} → {}: {e}", p.from, p.to))?;
        actions.push(Action {
            from: p.from.clone(),
            to: p.to.clone(),
            decision: d,
        });
        if d == Decision::Move {
            to_move.push(p);
        }
    }
    if dry_run || to_move.is_empty() {
        return Ok(actions);
    }

    for p in &to_move {
        dst_tf
            .state_mv(&p.from, &p.to, &src_file, &dst_file)
            .map_err(|e| format!("state mv {} → {}: {e}", p.from, p.to))?;
    }
    src_tf
        .state_push(&src_file, false)
        .map_err(|e| format!("push source state (concurrent change? never --force): {e}"))?;
    if let Err(e) = dst_tf.state_push(&dst_file, false) {
        // Half-applied: put the source back so the moved resources are not lost.
        let rb = tmp.path().join("source-rollback.tfstate");
        let rolled = write_private(&rb, &src_state).and_then(|()| src_tf.state_push(&rb, true));
        if let Err(rb_err) = rolled {
            let hint = match &deps.backup_dir {
                Some(d) => format!(
                    "restore from the backups under {}",
                    tfstackplan_cli::gofmt::quote(&d.to_string_lossy())
                ),
                None => {
                    "backups were disabled (no BackupDir) — no automatic backup to restore from"
                        .to_string()
                }
            };
            return Err(format!(
                "push dest state failed ({e}) and source rollback failed ({rb_err}): moved resources may be orphaned — {hint}"
            ));
        }
        return Err(format!(
            "push dest state failed, rolled the source back to its pre-move state: {e}"
        ));
    }
    Ok(actions)
}

/// Runs a release when dropped.
struct ReleaseGuard<'a>(Option<Release<'a>>);

impl Drop for ReleaseGuard<'_> {
    fn drop(&mut self) {
        if let Some(r) = self.0.take() {
            r();
        }
    }
}

/// Receives each move line with its destination stack (per-stack log streaming).
pub type Sink<'s> = &'s mut dyn FnMut(&str, &str);

/// Go's `applyPendingMoves`: discover every xmove manifest under `dir` and run
/// it (dry-run unless `execute`). Terraform is needed only when something is
/// pending; the first error stops the run. `build` makes the executor's
/// dependencies; `sink` gets each move line with its destination stack.
pub fn apply_pending_moves<'a>(
    dir: &str,
    execute_moves: bool,
    build: &dyn Fn() -> Result<ExecDeps<'a>, String>,
    out: &mut dyn Write,
    mut sink: Option<Sink<'_>>,
) -> Result<(), String> {
    let found: Vec<FoundXMove> = walk::discover_xmoves(dir)?;
    if found.is_empty() {
        return Ok(());
    }
    let deps = build().map_err(|e| format!("{} pending cross-state move(s): {e}", found.len()))?;
    for fx in &found {
        let actions =
            execute(&deps, dir, &fx.dest_stack, &fx.xmove, !execute_moves).map_err(|e| {
                format!(
                    "{} ({} → {}): {e}",
                    fx.key, fx.xmove.source_stack, fx.dest_stack
                )
            })?;
        for a in actions {
            let verb = match (a.decision, execute_moves) {
                (Decision::Skip, _) => "skip (already moved)",
                (Decision::Move, true) => "moved",
                (Decision::Move, false) => "would move",
            };
            let line = format!(
                "{}\t{} → {}\t{verb} {} → {}",
                fx.key, fx.xmove.source_stack, fx.dest_stack, a.from, a.to
            );
            let _ = writeln!(out, "{line}");
            let _ = out.flush();
            if let Some(s) = sink.as_mut() {
                s(&fx.dest_stack, &line);
            }
        }
    }
    Ok(())
}

#[cfg(test)]
mod tests;
