//! `state`: declarative state moves (Go's `cmd/tfstackplan/state.go` and
//! `statemanifest.go`): `move`, `import`, `remove`/`removed`, `list`,
//! `moves-manifest`, `cleanup`, `apply` and `check`.

use std::collections::BTreeMap;
use std::path::Path;
use std::process::ExitCode;

use tfstackplan_cli::flag::FlagSet;
use tfstackplan_cli::gofmt::quote;
use tfstackplan_cli::statemove::manifest::{
    collect_state_moves, manifest_json, resolve_source_stack,
};
use tfstackplan_cli::statemove::plan::{change_addresses, parse_plan};
use tfstackplan_cli::statemove::{
    self, AddressSet, Move, Op, Plan, Severity, XMove, check_xmove_source, classify_cross_stack,
    data_source_orphans, dest_providers, is_spent, merge_ops, parse_shim, parse_xmove, paths,
    prior_state_addrs, prior_state_data_sources, render_shim, render_xmove, shim_file_name,
    validate_move, validate_move_plan, xmove_file_name,
};

use super::{err, out, parse};
use crate::fsx::os_error;

mod exec;
mod terraform;
mod walk;

const STATE_USAGE: &str = "tfstackplan state: expected a subcommand (move|import|remove|removed|list|moves-manifest|cleanup|apply|check)\n";

/// Runs `state <subcommand>`.
pub fn run(args: &[String]) -> ExitCode {
    let Some(sub) = args.first() else {
        err(STATE_USAGE);
        return ExitCode::from(2);
    };
    let rest = &args[1..];
    match sub.as_str() {
        "move" => state_move(rest),
        "import" => state_import(rest),
        "remove" | "removed" => state_remove(rest),
        "list" => state_list(rest),
        "moves-manifest" => state_moves_manifest(rest),
        "cleanup" => state_cleanup(rest),
        "apply" => state_apply(rest),
        "check" => state_check(rest),
        other => {
            err(&format!(
                "tfstackplan state: unknown subcommand {}\n",
                quote(other)
            ));
            ExitCode::from(2)
        }
    }
}

fn fail(prefix: &str, e: &str) -> ExitCode {
    err(&format!("{prefix} {e}\n"));
    ExitCode::FAILURE
}

fn usage_err(msg: &str) -> ExitCode {
    err(&format!("{msg}\n"));
    ExitCode::from(2)
}

/// Go's `moveKey`: `--pr`, else `$TFSTACKPLAN_PR`, else the git branch, else `local`.
fn move_key(pr: &str, dir: &str) -> String {
    let pr = if pr.is_empty() {
        std::env::var("TFSTACKPLAN_PR").unwrap_or_default()
    } else {
        pr.to_string()
    };
    if !pr.is_empty() {
        return format!("PR-{pr}");
    }
    let out = std::process::Command::new("git")
        .args(["rev-parse", "--abbrev-ref", "HEAD"])
        .current_dir(dir)
        .stdin(std::process::Stdio::null())
        .stderr(std::process::Stdio::null())
        .output();
    if let Ok(o) = out
        && o.status.success()
    {
        let b = String::from_utf8_lossy(&o.stdout)
            .trim()
            .replace(['/', ' '], "-");
        if !b.is_empty() && b != "HEAD" {
            return format!("branch-{b}");
        }
    }
    "local".to_string()
}

/// Go's `stripStack`: an optional `stack:` prefix (addresses contain no `:`).
fn strip_stack(s: &str) -> (&str, &str) {
    match s.find(':') {
        Some(i) => (&s[..i], &s[i + 1..]),
        None => ("", s),
    }
}

fn load_state_plan(stack_dir: &str) -> Result<Plan, String> {
    let p = paths::join(&[stack_dir, "tfplan.json"]);
    let data = std::fs::read(&p).map_err(|e| {
        format!(
            "read plan: {} (run `tfstackplan run plan` first)",
            os_error("open", Path::new(&p), &e)
        )
    })?;
    parse_plan(&data).map_err(|e| format!("parse plan: {e}"))
}

const MOVE_USAGE: &str =
    "Usage: tfstackplan state move --dir DIR [--stack STACK] [--pr N] [--via mv] <from> <to> ...

Declares resource moves between stacks by writing manifest files into the
affected stack directories. The manifests are checked in alongside Terraform
code and executed automatically by 'run apply'.

Each <from> <to> pair is a resource or module address, optionally prefixed
with a stack name (stack:addr). --stack sets the default stack for unqualified
addresses.

Cross-stack modes:
  (default)  Emits native import{}/removed{} shims (--via import).
             Validated against tfplan.json at generation time.

  --via mv   Writes a _tfsp_xmove.<key>.hcl intent manifest in the destination
             stack. The manifest stores the from/to addresses you provide
             (module-level or resource-level). Concrete per-resource pairs are
             resolved at apply time from live state via 'terraform state mv'.
             Data sources (mode=data) are filtered out of all address walks —
             wildcards never sweep data.* into the move set.

             Use --via mv for module extractions, especially when the source
             stack contains a 'moved {}' block that renames the module (e.g.
             module.foo -> module.foo[0]) — the manifest's intent-level address
             matches the live state regardless of plan-time renames.

             Validation: a lightweight prior_state check runs at generation
             (verifying the from address exists in source state). Full
             validation — type compatibility, destination provider config —
             runs at classify time (the plan CI step) via 'run plan --classify'.
             Use 'state check --dir DIR' to validate manifests against local
             plan files at any time without re-running the classify pass.

             Data sources (mode=data) that fall under the from prefix will NOT
             be moved — they remain in the source stack. The classify pass emits
             a 'xmove/data-source-orphan' warning for each; run
             'terraform state rm <addr>' in the source stack to clean them up
             before retiring the source.

             The generated manifest includes a '# tfstackplan:key=<key>' header.
             Hand-authored manifests without the header are accepted; the key is
             derived from the filename (_tfsp_xmove.<key>.hcl).

Flags:
";

/// `state move`. Go walks its per-stack maps in random order when writing; this
/// writes stacks in sorted order, so the "wrote" lines are deterministic.
fn state_move(args: &[String]) -> ExitCode {
    let mut fs = FlagSet::new("state move");
    fs.string("dir", "", "terramate project root (required)")
        .string(
            "stack",
            "",
            "default stack for unqualified addresses (same-stack moves)",
        )
        .string(
            "pr",
            "",
            "PR number for the shim key (default: $TFSTACKPLAN_PR or git branch)",
        )
        .string(
            "via",
            "",
            "cross-stack mechanism: \"\" (native import/removed) or \"mv\" (faithful terraform state mv)",
        )
        .usage_header(MOVE_USAGE);
    let p = match parse(&fs, args) {
        Ok(p) => p,
        Err(c) => return c,
    };
    let dir = p.str("dir");
    if dir.is_empty() {
        return usage_err("state move: --dir is required");
    }
    let rest = &p.rest;
    if rest.is_empty() || rest.len() % 2 != 0 {
        return usage_err("state move: expected <from> <to> pairs");
    }
    let default_stack = p.str("stack");
    let via = p.str("via");

    let mut plans: BTreeMap<String, Plan> = BTreeMap::new();
    let mut load_for = |s: &str| -> Result<Plan, String> {
        if let Some(p) = plans.get(s) {
            return Ok(p.clone());
        }
        let p = load_state_plan(&paths::join(&[dir, s]))
            .map_err(|e| format!("stack {}: {e}", quote(s)))?;
        plans.insert(s.to_string(), p.clone());
        Ok(p)
    };
    let stack_of = |q: &str| -> Result<(String, String), String> {
        let (s, addr) = strip_stack(q);
        let s = if s.is_empty() { default_stack } else { s };
        if s.is_empty() {
            return Err(format!(
                "address {} has no stack (use stack:addr or --stack)",
                quote(q)
            ));
        }
        Ok((s.to_string(), addr.to_string()))
    };

    let mut ops_by_stack: BTreeMap<String, Vec<Op>> = BTreeMap::new();
    let mut xmove_by_dest: BTreeMap<String, XMove> = BTreeMap::new();
    for pair in rest.chunks(2) {
        let (from, to) = (stack_of(&pair[0]), stack_of(&pair[1]));
        let ((from_stack, from_addr), (to_stack, to_addr)) = match (from, to) {
            (Ok(f), Ok(t)) => (f, t),
            (Err(e), _) | (_, Err(e)) => return usage_err(&format!("state move: {e}")),
        };
        if from_stack == to_stack {
            let plan = match load_for(&from_stack) {
                Ok(p) => p,
                Err(e) => return fail("state move:", &e),
            };
            if let Err(e) = validate_move(&plan, &from_addr, &to_addr) {
                return fail("state move:", &e);
            }
            ops_by_stack
                .entry(from_stack)
                .or_default()
                .push(Op::moved(&from_addr, &to_addr));
            continue;
        }
        let src_plan = match load_for(&from_stack) {
            Ok(p) => p,
            Err(e) => return fail("state move:", &e),
        };
        if via == "mv" {
            if let Err(e) = check_xmove_source(&src_plan, &from_addr) {
                return fail("state move:", &e);
            }
            let existing = xmove_by_dest.entry(to_stack.clone()).or_default();
            if !existing.source_stack.is_empty() && existing.source_stack != from_stack {
                return fail(
                    "state move:",
                    &format!(
                        "dest stack {} already targets source {}; cannot also pull from {} (one manifest per dest+source)",
                        quote(&to_stack),
                        quote(&existing.source_stack),
                        quote(&from_stack)
                    ),
                );
            }
            existing.source_stack = from_stack;
            existing.pairs.push(Move::new(&from_addr, &to_addr));
            continue;
        }
        let dst_plan = match load_for(&to_stack) {
            Ok(p) => p,
            Err(e) => return fail("state move:", &e),
        };
        match classify_cross_stack(&src_plan, &dst_plan, &from_addr, &to_addr) {
            Ok((src_ops, dst_ops)) => {
                ops_by_stack.entry(from_stack).or_default().extend(src_ops);
                ops_by_stack.entry(to_stack).or_default().extend(dst_ops);
            }
            Err(e) => return fail("state move:", &e),
        }
    }

    let key = move_key(p.str("pr"), dir);
    for (s, ops) in &ops_by_stack {
        if let Err(e) = write_shim_file(dir, s, &key, ops) {
            return fail("state move:", &e);
        }
    }
    for (dest, xm) in &xmove_by_dest {
        let manifest_path = paths::join(&[dir, dest, &xmove_file_name(&key)]);
        let mut merged = xm.clone();
        if let Ok(data) = std::fs::read_to_string(&manifest_path) {
            let prev = match parse_xmove(&data) {
                Ok((_, prev)) => prev,
                Err(e) => {
                    return fail(
                        "state move:",
                        &format!("parse existing {manifest_path}: {e}"),
                    );
                }
            };
            if prev.source_stack != xm.source_stack {
                return fail(
                    "state move:",
                    &format!(
                        "existing manifest {manifest_path} targets source {}, not {}",
                        quote(&prev.source_stack),
                        quote(&xm.source_stack)
                    ),
                );
            }
            merged.pairs = merge_moves(&prev.pairs, &xm.pairs);
        }
        if let Err(e) = std::fs::write(&manifest_path, render_xmove(&key, &merged)) {
            return fail(
                "state move: write xmove:",
                &os_error("open", Path::new(&manifest_path), &e),
            );
        }
        out(&format!(
            "wrote {manifest_path} ({} move(s))\n",
            merged.pairs.len()
        ));
    }
    ExitCode::SUCCESS
}

/// Go's `mergeMoves`: `existing` then `add`, de-duplicated, order kept.
fn merge_moves(existing: &[Move], add: &[Move]) -> Vec<Move> {
    let mut out: Vec<Move> = Vec::new();
    for m in existing.iter().chain(add) {
        if !out.contains(m) {
            out.push(m.clone());
        }
    }
    out
}

fn write_shim_file(dir: &str, stack: &str, key: &str, ops: &[Op]) -> Result<(), String> {
    let shim_path = paths::join(&[dir, stack, &shim_file_name(key)]);
    let mut existing = Vec::new();
    if let Ok(data) = std::fs::read_to_string(&shim_path) {
        match parse_shim(&data) {
            Ok((_, ex)) => existing = ex,
            Err(e) => return Err(format!("parse existing shim {shim_path}: {e}")),
        }
    }
    let merged = merge_ops(&existing, ops);
    std::fs::write(&shim_path, render_shim(key, &merged)).map_err(|e| {
        format!(
            "write shim: {}",
            os_error("open", Path::new(&shim_path), &e)
        )
    })?;
    out(&format!("wrote {shim_path} ({} op(s))\n", merged.len()));
    Ok(())
}

fn state_list(args: &[String]) -> ExitCode {
    let mut fs = FlagSet::new("state list");
    fs.string("dir", "", "terramate project root (required)")
        .string("pr", "", "only this PR's moves")
        .bool("all", false, "list all moves (default)");
    let p = match parse(&fs, args) {
        Ok(p) => p,
        Err(c) => return c,
    };
    let dir = p.str("dir");
    if dir.is_empty() {
        return usage_err("state list: --dir is required");
    }
    let shims = match walk::discover(dir) {
        Ok(s) => s,
        Err(e) => return fail("state list:", &e),
    };
    let want = if p.str("pr").is_empty() {
        String::new()
    } else {
        format!("PR-{}", p.str("pr"))
    };
    let mut text = String::new();
    for s in &shims {
        if !want.is_empty() && s.key != want {
            continue;
        }
        for o in &s.ops {
            match o.kind.as_str() {
                "moved" => text.push_str(&format!(
                    "{}\t{}\tmoved {} → {}\n",
                    s.key, s.stack, o.from, o.to
                )),
                "import" => text.push_str(&format!(
                    "{}\t{}\timport {} (id={})\n",
                    s.key, s.stack, o.to, o.id
                )),
                "removed" => {
                    text.push_str(&format!("{}\t{}\tremoved {}\n", s.key, s.stack, o.from));
                }
                _ => {}
            }
        }
    }
    out(&text);
    ExitCode::SUCCESS
}

/// Go's `collectStateMoves` with the filesystem filled in.
fn collect(dir: &str, want: &str) -> Result<BTreeMap<String, Vec<String>>, String> {
    let shims = walk::discover(dir)?;
    let xmoves = walk::discover_xmoves(dir)?;
    let exists = |rel: &str| std::fs::metadata(paths::join(&[dir, rel])).is_ok();
    Ok(collect_state_moves(&shims, &xmoves, want, &exists))
}

fn state_moves_manifest(args: &[String]) -> ExitCode {
    let mut fs = FlagSet::new("state moves-manifest");
    fs.string("dir", "", "terramate project root (required)")
        .string("pr", "", "only this PR's moves (default: all)")
        .string("o", "", "write JSON to this file (default: stdout)");
    let p = match parse(&fs, args) {
        Ok(p) => p,
        Err(c) => return c,
    };
    let dir = p.str("dir");
    if dir.is_empty() {
        return usage_err("state moves-manifest: --dir is required");
    }
    let want = if p.str("pr").is_empty() {
        String::new()
    } else {
        format!("PR-{}", p.str("pr"))
    };
    let manifest = match collect(dir, &want) {
        Ok(m) => m,
        Err(e) => return fail("state moves-manifest:", &e),
    };
    let json = manifest_json(&manifest);
    let o = p.str("o");
    if o.is_empty() {
        out(&json);
        return ExitCode::SUCCESS;
    }
    if let Err(e) = std::fs::write(o, json) {
        return fail("state moves-manifest:", &os_error("open", Path::new(o), &e));
    }
    ExitCode::SUCCESS
}

fn state_cleanup(args: &[String]) -> ExitCode {
    let mut fs = FlagSet::new("state cleanup");
    fs.string("dir", "", "terramate project root (required)")
        .string("pr", "", "remove only this PR's shims")
        .bool(
            "all",
            false,
            "remove ALL tfstackplan move shims and xmove manifests in the tree",
        )
        .bool(
            "applied",
            false,
            "remove all xmove manifests (cross-state move manifests whose apply has been verified); same-stack shims are left intact",
        );
    let p = match parse(&fs, args) {
        Ok(p) => p,
        Err(c) => return c,
    };
    let dir = p.str("dir");
    if dir.is_empty() {
        return usage_err("state cleanup: --dir is required");
    }
    if p.bool("applied") {
        return match walk::cleanup_xmoves(dir, "") {
            Ok(n) => {
                out(&format!("removed {n} xmove manifest(s)\n"));
                ExitCode::SUCCESS
            }
            Err(e) => fail("state cleanup xmoves:", &e),
        };
    }
    let pr = p.str("pr");
    if pr.is_empty() == !p.bool("all") {
        return usage_err("state cleanup: pass exactly one of --pr <n>, --all, or --applied");
    }
    let key = if pr.is_empty() {
        String::new()
    } else {
        format!("PR-{pr}")
    };
    let n = match walk::cleanup(dir, &key) {
        Ok(n) => n,
        Err(e) => return fail("state cleanup:", &e),
    };
    let nx = match walk::cleanup_xmoves(dir, &key) {
        Ok(n) => n,
        Err(e) => return fail("state cleanup xmoves:", &e),
    };
    out(&format!(
        "removed {n} shim file(s), {nx} xmove manifest(s)\n"
    ));
    ExitCode::SUCCESS
}

/// The GCS state lock behind `state apply --lock`.
struct GcsLocker(tfstackplan_clients::gcs::Locker);

impl exec::Locker for GcsLocker {
    fn acquire(&self, stack_dir: &str) -> Result<exec::Release<'_>, String> {
        let files = walk::tf_files(stack_dir);
        let Some((bucket, prefix)) =
            statemove::backend::gcs_backend(files.iter().map(String::as_str))
        else {
            return Err(format!("no gcs backend configured in {stack_dir}"));
        };
        let object = format!("{prefix}/default.tflock");
        let info = tfstackplan_clients::gcs::LockInfo {
            id: rand_id(),
            operation: "tfstackplan state apply".into(),
            who: who(),
            version: "tfstackplan".into(),
            created: rfc3339_nano_utc(std::time::SystemTime::now()),
            path: format!("gs://{bucket}/{object}"),
        };
        let lock = self.0.acquire(stack_dir, &bucket, &prefix, &info)?;
        Ok(Box::new(move || {
            let _ = lock.release();
        }))
    }
}

/// 16 random bytes, hex (Go's `randID`).
fn rand_id() -> String {
    use std::io::Read;
    let mut b = [0u8; 16];
    if let Ok(mut f) = std::fs::File::open("/dev/urandom") {
        let _ = f.read_exact(&mut b);
    }
    b.iter().map(|x| format!("{x:02x}")).collect()
}

/// `user@host`, as Go's `os/user` (pure Go: `/etc/passwd` by uid) and `os.Hostname`
/// find them; `tfstackplan` when the user is unknown.
fn who() -> String {
    use std::os::unix::fs::MetadataExt;
    let mut who = "tfstackplan".to_string();
    if let Ok(m) = std::fs::metadata("/proc/self")
        && let Ok(passwd) = std::fs::read_to_string("/etc/passwd")
    {
        let uid = m.uid().to_string();
        if let Some(name) = passwd.lines().find_map(|l| {
            let f: Vec<&str> = l.split(':').collect();
            (f.len() > 2 && f[2] == uid && !f[0].is_empty()).then(|| f[0].to_string())
        }) {
            who = name;
        }
    }
    if let Ok(h) = std::fs::read_to_string("/proc/sys/kernel/hostname") {
        let h = h.trim();
        if !h.is_empty() {
            who.push('@');
            who.push_str(h);
        }
    }
    who
}

/// `t.UTC().Format(time.RFC3339Nano)`: trailing zero nanoseconds trimmed.
fn rfc3339_nano_utc(t: std::time::SystemTime) -> String {
    let d = t.duration_since(std::time::UNIX_EPOCH).unwrap_or_default();
    let secs = d.as_secs();
    let days = i64::try_from(secs / 86_400).unwrap_or_default();
    let rem = secs % 86_400;
    // Civil date from days since 1970-01-01 (Howard Hinnant's algorithm).
    let z = days + 719_468;
    let era = z.div_euclid(146_097);
    let doe = z - era * 146_097;
    let yoe = (doe - doe / 1460 + doe / 36_524 - doe / 146_096) / 365;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let day = doy - (153 * mp + 2) / 5 + 1;
    let month = if mp < 10 { mp + 3 } else { mp - 9 };
    let year = yoe + era * 400 + i64::from(month <= 2);
    let mut s = format!(
        "{year:04}-{month:02}-{day:02}T{:02}:{:02}:{:02}",
        rem / 3600,
        rem % 3600 / 60,
        rem % 60
    );
    let nanos = d.subsec_nanos();
    if nanos > 0 {
        let frac = format!("{nanos:09}");
        s.push('.');
        s.push_str(frac.trim_end_matches('0'));
    }
    s.push('Z');
    s
}

fn state_apply(args: &[String]) -> ExitCode {
    let mut fs = FlagSet::new("state apply");
    fs.string("dir", "", "terramate project root (required)")
        .bool(
            "execute",
            false,
            "perform the moves (default: dry-run, print only)",
        )
        .bool(
            "lock",
            false,
            "acquire a pessimistic GCS state lock around each move (fail-fast if already locked; requires ADC)",
        );
    let p = match parse(&fs, args) {
        Ok(p) => p,
        Err(c) => return c,
    };
    let dir = p.str("dir");
    if dir.is_empty() {
        return usage_err("state apply: --dir is required");
    }
    let execute = p.bool("execute");
    let mut locker: Option<GcsLocker> = None;
    if p.bool("lock") && execute {
        let built = tfstackplan_clients::google::access_token_source()
            .and_then(|token| tfstackplan_clients::gcs::Locker::new(token, ""));
        match built {
            Ok(l) => locker = Some(GcsLocker(l)),
            Err(e) => return fail("state apply: --lock:", &e),
        }
    }
    let locker_ref: Option<&dyn exec::Locker> = match &locker {
        Some(l) => Some(l),
        None => None,
    };
    let build = || -> Result<exec::ExecDeps<'_>, String> {
        let tf_path = terraform::look_path("terraform")
            .map_err(|e| format!("terraform not found on PATH: {e}"))?;
        // Backups go to the OS temp space so nothing lands in the repo; the
        // directory is kept for a manual restore.
        let backup_dir = tempfile::Builder::new()
            .prefix("tfsp-state-backups-")
            .tempdir()
            .map_err(|e| format!("create backup dir: {e}"))?
            .keep();
        err(&format!("state backups: {}\n", backup_dir.display()));
        Ok(exec::ExecDeps {
            new_tf: Box::new(move |wd: &str| {
                let t: Box<dyn exec::Runner> = Box::new(terraform::Terraform::new(&tf_path, wd)?);
                Ok(t)
            }),
            backup_dir: Some(backup_dir),
            locker: locker_ref,
        })
    };
    let mut stdout = std::io::stdout();
    if let Err(e) = exec::apply_pending_moves(dir, execute, &build, &mut stdout, None) {
        return fail("state apply:", &e);
    }
    ExitCode::SUCCESS
}

const CHECK_USAGE: &str = "Usage: tfstackplan state check --dir DIR

Validates all pending xmove manifests against the local tfplan.json files
produced by 'run plan'. Read-only: does not run terraform, does not mutate files.

For each manifest reports: spent | valid | xmove/<error-code>
Exit 0 when all manifests are valid or spent.

Flags:
";

/// `state check`: validates every xmove manifest against the local plan files.
fn state_check(args: &[String]) -> ExitCode {
    let mut fs = FlagSet::new("state check");
    fs.string("dir", "", "terramate project root (required)")
        .usage_header(CHECK_USAGE);
    let p = match parse(&fs, args) {
        Ok(p) => p,
        Err(c) => return c,
    };
    let dir = p.str("dir");
    if dir.is_empty() {
        return usage_err("state check: --dir is required");
    }
    let xmoves = match walk::discover_xmoves(dir) {
        Ok(x) => x,
        Err(e) => return fail("state check:", &e),
    };
    let mut has_errors = false;
    for fx in &xmoves {
        let exists = |rel: &str| std::fs::metadata(paths::join(&[dir, rel])).is_ok();
        let source = resolve_source_stack(&fx.dest_stack, &fx.xmove.source_stack, &exists);
        let src_plan_path = paths::join(&[dir, &source, "tfplan.json"]);
        let dst_plan_path = paths::join(&[dir, &fx.dest_stack, "tfplan.json"]);
        let dst_bytes = std::fs::read(&dst_plan_path).ok();
        let dst_prior: AddressSet = dst_bytes
            .as_deref()
            .and_then(prior_state_addrs)
            .unwrap_or_default();

        let Ok(src_bytes) = std::fs::read(&src_plan_path) else {
            if is_spent(&fx.xmove.pairs, &dst_prior) {
                err(&format!(
                    "\u{2139}\u{fe0f}  xmove {}: xmove/spent — all declared moves already applied; run 'state cleanup --applied' to remove this manifest\n",
                    fx.key
                ));
            } else {
                err(&format!(
                    "\u{274c} xmove {}: xmove/source-not-planned — source stack {} has no tfplan.json; run 'run plan' first, or remove this manifest if the move is already complete\n",
                    fx.key,
                    quote(&source)
                ));
                has_errors = true;
            }
            continue;
        };

        let Some(prior) = prior_state_addrs(&src_bytes) else {
            err(&format!(
                "\u{274c} xmove {}: source stack {} has no prior_state (stack is new — nothing to move)\n",
                fx.key,
                quote(&source)
            ));
            has_errors = true;
            continue;
        };

        let data_sources = prior_state_data_sources(&src_bytes).unwrap_or_default();
        for ds in data_source_orphans(&fx.xmove.pairs, &data_sources) {
            err(&format!(
                "\u{26a0}\u{fe0f}  xmove {}: xmove/data-source-orphan — {} will remain in the source stack (run 'terraform state rm {ds}' to clean up)\n",
                fx.key,
                quote(&ds)
            ));
        }

        let dst_addrs = dst_bytes
            .as_deref()
            .and_then(|b| parse_plan(b).ok())
            .map(|p| change_addresses(&p))
            .unwrap_or_default();
        let dest_dir = paths::join(&[dir, &fx.dest_stack]);
        let providers = dest_providers(walk::tf_files(&dest_dir).iter().map(String::as_str));
        let diags =
            validate_move_plan(&prior, &dst_addrs, &dst_prior, &providers, &fx.xmove, false);
        if diags.is_empty() {
            err(&format!("\u{2705} xmove {}: valid\n", fx.key));
            continue;
        }
        for d in &diags {
            if d.severity == Severity::Error {
                err(&format!(
                    "\u{274c} xmove {}: {} — {}\n",
                    fx.key, d.code, d.message
                ));
                has_errors = true;
            } else if d.code == "xmove/spent" {
                err(&format!(
                    "\u{2139}\u{fe0f}  xmove {}: {} — {}\n",
                    fx.key, d.code, d.message
                ));
            } else {
                err(&format!(
                    "\u{26a0}\u{fe0f}  xmove {}: {} — {}\n",
                    fx.key, d.code, d.message
                ));
            }
        }
    }
    if has_errors {
        ExitCode::FAILURE
    } else {
        ExitCode::SUCCESS
    }
}

fn state_import(args: &[String]) -> ExitCode {
    let mut fs = FlagSet::new("state import");
    fs.string("dir", "", "terramate project root (required)")
        .string("stack", "", "destination stack for import block (required)")
        .string("pr", "", "PR number for the shim key");
    let p = match parse(&fs, args) {
        Ok(p) => p,
        Err(c) => return c,
    };
    let (dir, stack) = (p.str("dir"), p.str("stack"));
    if dir.is_empty() || stack.is_empty() {
        return usage_err("state import: --dir and --stack are required");
    }
    if p.rest.is_empty() || p.rest.len() % 2 != 0 {
        return usage_err("state import: expected <to-addr> <import-id> pairs");
    }
    let ops: Vec<Op> = p.rest.chunks(2).map(|c| Op::import(&c[0], &c[1])).collect();
    let key = move_key(p.str("pr"), dir);
    if let Err(e) = write_shim_file(dir, stack, &key, &ops) {
        return fail("state import:", &e);
    }
    ExitCode::SUCCESS
}

fn state_remove(args: &[String]) -> ExitCode {
    let mut fs = FlagSet::new("state remove");
    fs.string("dir", "", "terramate project root (required)")
        .string("stack", "", "source stack for removed block (required)")
        .string("pr", "", "PR number for the shim key");
    let p = match parse(&fs, args) {
        Ok(p) => p,
        Err(c) => return c,
    };
    let (dir, stack) = (p.str("dir"), p.str("stack"));
    if dir.is_empty() || stack.is_empty() {
        return usage_err("state remove: --dir and --stack are required");
    }
    if p.rest.is_empty() {
        return usage_err("state remove: expected at least one resource address");
    }
    let ops: Vec<Op> = p.rest.iter().map(|a| Op::removed(a)).collect();
    let key = move_key(p.str("pr"), dir);
    if let Err(e) = write_shim_file(dir, stack, &key, &ops) {
        return fail("state remove:", &e);
    }
    ExitCode::SUCCESS
}

#[cfg(test)]
mod tests;
