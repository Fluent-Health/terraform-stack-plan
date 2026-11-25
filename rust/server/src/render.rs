//! The pure half of the check-run surface (Go: `render.go`, `viewdata.go`,
//! `triage.go`, `status.go` and the title helpers of `checkrun.go`): progress bars,
//! titles, summaries, the failures and approval sections and the verdict. No I/O.

use std::fmt::Write as _;

use tfstackplan_gate::events::{Category, Counts, Graph, Phase, StackState, Status};
use tfstackplan_store::GateTargetRow;

use crate::config::{PhaseWeight, ProgressConfig};

/// Whether a stack has finished its work for progress accounting.
pub fn done(s: Status) -> bool {
    matches!(
        s,
        Status::Planned
            | Status::Safe
            | Status::Nochange
            | Status::Moving
            | Status::Gated
            | Status::Failed
            | Status::Aborted
    )
}

/// The aggregate op tally across an execution's stacks.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub struct Verdict {
    pub add: i64,
    pub change: i64,
    pub destroy: i64,
    pub replace: i64,
    pub moved: i64,
    pub import: i64,
    pub forget: i64,
    pub total_ops: i64,
}

pub fn aggregate_verdict(stacks: &[StackState]) -> Verdict {
    let mut v = Verdict::default();
    for c in stacks.iter().filter_map(|s| s.counts.as_ref()) {
        v.add += c.add;
        v.change += c.change;
        v.destroy += c.destroy;
        v.replace += c.replace;
        v.moved += c.moved;
        v.import += c.import;
        v.forget += c.forget;
    }
    v.total_ops = v.add + v.change + v.destroy + v.replace;
    v
}

/// "+A ~C ±R −D ↔M", only the kinds present; "" when none.
pub fn op_tally(v: Verdict) -> String {
    let mut parts = Vec::new();
    if v.add > 0 {
        parts.push(format!("+{}", v.add));
    }
    if v.change > 0 {
        parts.push(format!("~{}", v.change));
    }
    if v.replace > 0 {
        parts.push(format!("±{}", v.replace));
    }
    if v.destroy > 0 {
        parts.push(format!("−{}", v.destroy));
    }
    if v.moved > 0 {
        parts.push(format!("↔{}", v.moved));
    }
    parts.join(" ")
}

/// The number of stacks carrying the gating "iam" category.
pub fn iam_count(stacks: &[StackState]) -> usize {
    stacks
        .iter()
        .filter(|s| s.categories.iter().any(|c| c.name == "iam"))
        .count()
}

/// A per-stack risk chip: (label, css).
pub fn risk_tags(s: &StackState) -> Vec<(&'static str, &'static str)> {
    s.categories
        .iter()
        .filter_map(|c| match c.name.as_str() {
            "iam" => Some(("⚿ IAM", "iam")),
            "destructive" => Some(("⚠ destructive", "danger")),
            _ => None,
        })
        .collect()
}

/// Whether the execution is in the real apply (or the verify after it).
pub fn apply_started(p: &Phase) -> bool {
    p.as_str() == Phase::APPLYING || p.as_str() == Phase::VERIFYING
}

/// The viewer's per-state label for a stack status in a plan or apply.
pub fn display_state(st: Status, kind: &str, phase: &Phase) -> String {
    let applying = kind == "apply" && apply_started(phase);
    match st {
        Status::Pending => "queued",
        Status::Initializing => "initializing",
        Status::Initialized => "initialized",
        Status::Running if kind == "apply" && applying => "applying",
        Status::Running if kind == "apply" => "preparing",
        Status::Running => "planning",
        Status::Planned if kind == "apply" && applying => "queued",
        Status::Planned if kind == "apply" => "prepared",
        Status::Planned => "planned",
        Status::Moving => "moving",
        Status::Gated => "blocked",
        Status::Safe if kind == "apply" => "applied",
        Status::Safe => "planned",
        Status::Failed => "failed",
        Status::Nochange => "no changes",
        Status::Aborted => "aborted",
        Status::Unset => return String::new(),
    }
    .to_string()
}

/// The dominant op kind of one stack ("+3"), "" when none.
pub fn op_summary(c: Option<&Counts>) -> String {
    let Some(c) = c else { return String::new() };
    if c.add > 0 {
        format!("+{}", c.add)
    } else if c.change > 0 {
        format!("~{}", c.change)
    } else if c.replace > 0 {
        format!("±{}", c.replace)
    } else if c.destroy > 0 {
        format!("−{}", c.destroy)
    } else if c.moved > 0 {
        format!("↔{}", c.moved)
    } else {
        String::new()
    }
}

/// Go's `url.QueryEscape`.
pub fn query_escape(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    for b in s.bytes() {
        match b {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'_' | b'.' | b'~' => {
                out.push(char::from(b))
            }
            b' ' => out.push('+'),
            _ => {
                let _ = write!(out, "%{b:02X}");
            }
        }
    }
    out
}

/// The PAM console's pending-approvals tab for a target project.
pub fn pam_console_url(target: &str) -> String {
    format!(
        "https://console.cloud.google.com/iam-admin/pam/grants/approvals?project={}",
        query_escape(target)
    )
}

/// The "awaiting approval" banner, "" when every gate is active.
pub fn gates_section(targets: &[GateTargetRow]) -> String {
    let pending: Vec<_> = targets.iter().filter(|t| t.state != "ACTIVE").collect();
    if pending.is_empty() {
        return String::new();
    }
    let mut b = String::from(
        "### ⏳ Awaiting approval\n\nThis plan changes gated resources. Merge is blocked until each grant is approved in PAM:\n\n",
    );
    for t in pending {
        let _ = writeln!(
            b,
            "- 🔐 `{}` · `{}` — [approve in PAM ↗]({})",
            t.class,
            t.target,
            pam_console_url(&t.target)
        );
    }
    b.push_str("\n---\n\n");
    b
}

/// The bar, label and percentage for a phase and stack counts.
pub fn progress(
    phases: &[PhaseWeight],
    phase: &Phase,
    planned: usize,
    initialized: usize,
    total: usize,
) -> (String, String, i64) {
    let label = phase_label(phase, planned, initialized, total);
    let frac = weighted_frac(phases, phase, planned, initialized, total)
        .unwrap_or_else(|| legacy_frac(phase, planned, initialized, total));
    let (bar, pct) = progress_bar(frac);
    (bar, label, pct)
}

#[expect(
    clippy::cast_precision_loss,
    reason = "stack counts are small; Go converts the same way"
)]
fn ratio(n: usize, d: usize) -> f64 {
    n as f64 / d as f64
}

/// The fraction across a configured weighted phase set; `None` when the set is
/// empty or does not contain the phase.
pub fn weighted_frac(
    phases: &[PhaseWeight],
    phase: &Phase,
    planned: usize,
    initialized: usize,
    total: usize,
) -> Option<f64> {
    if phases.is_empty() {
        return None;
    }
    let total_w: f64 = phases.iter().map(|p| p.weight).sum();
    let mut cum = 0.0;
    let mut found = false;
    for pw in phases {
        if pw.phase == *phase {
            let mut sub = 1.0;
            if phase.ticking() && total > 0 {
                let n = if phase.as_str() == Phase::INITIALIZING {
                    initialized
                } else {
                    planned
                };
                sub = ratio(n, total);
            }
            cum += pw.weight * sub;
            found = true;
            break;
        }
        cum += pw.weight;
    }
    if !found || total_w <= 0.0 {
        return None;
    }
    Some(cum / total_w)
}

/// The built-in fraction when no progress block is configured.
pub fn legacy_frac(phase: &Phase, planned: usize, initialized: usize, total: usize) -> f64 {
    match phase.as_str() {
        Phase::IMAGE => 0.02,
        Phase::WARMING | Phase::LINTING => 0.05,
        Phase::CLAIM => 0.10,
        Phase::INITIALIZING => {
            if total > 0 && initialized > 0 {
                0.05 + 0.10 * ratio(initialized, total)
            } else {
                0.15
            }
        }
        Phase::MOVES => 0.15,
        Phase::APPLYING => {
            if total > 0 {
                ratio(planned, total)
            } else {
                0.0
            }
        }
        Phase::CLASSIFY => 0.95,
        Phase::REPORT => 0.98,
        Phase::TESTING | Phase::VERIFYING => 1.0,
        _ => {
            if total == 0 {
                0.20
            } else if planned >= total {
                1.0
            } else {
                0.20 + 0.80 * ratio(planned, total)
            }
        }
    }
}

/// The human label for a phase and counts.
pub fn phase_label(phase: &Phase, planned: usize, initialized: usize, total: usize) -> String {
    match phase.as_str() {
        Phase::IMAGE => "preparing runner image…".into(),
        Phase::WARMING => "warming cache…".into(),
        Phase::LINTING => "linting modules…".into(),
        Phase::CLAIM => "acquiring apply lock…".into(),
        Phase::INITIALIZING => {
            if total > 0 && initialized > 0 {
                format!("initialized {initialized}/{total}")
            } else {
                format!("initializing {total} stacks…")
            }
        }
        Phase::MOVES => "executing state moves…".into(),
        Phase::APPLYING => {
            if total > 0 && planned == total {
                format!("applied {planned}/{total}")
            } else {
                format!("applying {planned}/{total}")
            }
        }
        Phase::CLASSIFY => "classifying plans…".into(),
        Phase::REPORT => "generating report…".into(),
        Phase::TESTING => "testing…".into(),
        Phase::VERIFYING => "verifying…".into(),
        _ => {
            if total == 0 {
                "planning…".into()
            } else if planned >= total {
                "planned".into()
            } else {
                format!("planning {planned}/{total}")
            }
        }
    }
}

const BRAILLE_SUB_BLOCKS: [&str; 5] = ["⠄", "⠆", "⠇", "⠧", "⠷"];
const PROGRESS_CELLS: i64 = 10;

/// The 10-cell braille bar and integer percentage for a 0..1 fraction.
#[expect(
    clippy::cast_possible_truncation,
    clippy::cast_sign_loss,
    reason = "Go truncates the same way; the fraction is clamped to 0..1"
)]
pub fn progress_bar(frac: f64) -> (String, i64) {
    let frac = frac.clamp(0.0, 1.0);
    let val = frac * PROGRESS_CELLS as f64;
    let mut full = val as i64;
    let rem = val - full as f64;
    let mut index = (rem * 6.0 + 0.5) as i64;
    if index == 6 {
        full += 1;
        index = 0;
    }
    let mut b = "⠿".repeat(full as usize);
    if index > 0 && full < PROGRESS_CELLS {
        b.push_str(BRAILLE_SUB_BLOCKS[(index - 1) as usize]);
    }
    let mut empty = PROGRESS_CELLS - full;
    if index > 0 {
        empty -= 1;
    }
    if empty > 0 {
        b.push_str(&"\u{2800}".repeat(empty as usize));
    }
    (b, (frac * 100.0 + 0.5) as i64)
}

/// Stacks that finished their work.
pub fn done_stacks(stacks: &[StackState]) -> usize {
    stacks.iter().filter(|s| done(s.status)).count()
}

/// The live progress bar, percentage and label of a running execution.
pub fn run_progress(
    prog: Option<&ProgressConfig>,
    phase: &Phase,
    stacks: &[StackState],
    kind: &str,
) -> (String, i64, String) {
    let total = stacks.len();
    let done_count = done_stacks(stacks);
    let init_count = stacks
        .iter()
        .filter(|s| s.status == Status::Initialized)
        .count();
    let preparing = kind == "apply" && !apply_started(phase);
    let bar_phase = if preparing && phase.as_str().is_empty() {
        Phase::new(Phase::WARMING)
    } else {
        phase.clone()
    };
    let weights = prog.map(|p| p.for_kind(kind)).unwrap_or_default();
    let (bar, label, pct) = progress(weights, &bar_phase, done_count, init_count, total);
    if preparing {
        if total == 0 {
            return (bar, pct, "preparing".into());
        }
        return (bar, pct, format!("preparing {done_count}/{total}"));
    }
    (bar, pct, label)
}

/// The check-run title: the live bar while running, a tally once terminal.
pub fn progress_title(
    prog: Option<&ProgressConfig>,
    phase: &Phase,
    stacks: &[StackState],
    terminal: bool,
    kind: &str,
) -> String {
    if terminal {
        return terminal_summary(if kind == "apply" { "Apply" } else { "Plan" }, stacks);
    }
    let (bar, _, label) = run_progress(prog, phase, stacks, kind);
    format!("{bar} · {label}")
}

/// The concluded-run title: kind, tally, stack count and failure suffixes.
pub fn terminal_summary(kind: &str, stacks: &[StackState]) -> String {
    let (mut failed, mut aborted, mut nochange, mut applied) = (0, 0, 0, 0);
    for s in stacks {
        match s.status {
            Status::Failed => failed += 1,
            Status::Aborted => aborted += 1,
            Status::Nochange => {
                nochange += 1;
                applied += 1;
            }
            Status::Safe => applied += 1,
            _ => {}
        }
    }
    let tally = op_tally(aggregate_verdict(stacks));
    let mut head = if stacks.is_empty() {
        format!("{kind} · no changes")
    } else if kind == "Apply" {
        let mut h = format!("{kind} · applied {applied}/{}", stacks.len());
        if !tally.is_empty() {
            h.push_str(" · ");
            h.push_str(&tally);
        }
        h
    } else if tally.is_empty() {
        format!("{kind} · no changes")
    } else {
        format!("{kind} · {tally} · {} stacks", stacks.len())
    };
    if nochange > 0 && kind == "Apply" {
        let _ = write!(head, " · {nochange} no-change");
    }
    if failed > 0 {
        let _ = write!(head, " · {failed} failed");
    }
    if aborted > 0 {
        let _ = write!(head, " · {aborted} aborted");
    }
    head
}

/// The check-run summary: verdict chips and a per-stack table.
pub fn check_summary(kind: &str, stacks: &[StackState], viewer_url: &str, phase: &Phase) -> String {
    let mut b = String::new();
    let mut chips = Vec::new();
    if stacks
        .iter()
        .any(|s| risk_tags(s).iter().any(|(_, css)| *css == "danger"))
    {
        chips.push("⚠️ destructive".to_string());
    }
    let n = iam_count(stacks);
    if n > 0 {
        chips.push(format!("⚿ {n} IAM"));
    }
    if !viewer_url.is_empty() {
        chips.push(format!("[live viewer ↗]({viewer_url})"));
    }
    if !chips.is_empty() {
        b.push_str(&chips.join(" · "));
        b.push('\n');
    }
    b.push('\n');
    if !stacks.is_empty() {
        b.push_str("| Stack | Ops | Risk | State |\n|---|---|---|---|\n");
        for s in stacks {
            let risks: Vec<_> = risk_tags(s).into_iter().map(|(l, _)| l).collect();
            let _ = writeln!(
                b,
                "| `{}` | {} | {} | {} |",
                s.path,
                op_summary(s.counts.as_ref()),
                risks.join(" "),
                display_state(s.status, kind, phase)
            );
        }
    }
    b
}

/// Go's `ansi.Strip`: removes SGR escape sequences (`\x1b[...m`).
pub fn ansi_strip(s: &str) -> String {
    let bytes = s.as_bytes();
    let mut out = Vec::with_capacity(bytes.len());
    let mut i = 0;
    while i < bytes.len() {
        if bytes[i] == 0x1b && bytes.get(i + 1) == Some(&b'[') {
            let mut j = i + 2;
            while j < bytes.len() && (bytes[j].is_ascii_digit() || bytes[j] == b';') {
                j += 1;
            }
            if j < bytes.len() && bytes[j] == b'm' {
                i = j + 1;
                continue;
            }
        }
        out.push(bytes[i]);
        i += 1;
    }
    String::from_utf8_lossy(&out).into_owned()
}

fn trim_right(s: &str, cut: &[char]) -> String {
    s.trim_end_matches(cut).to_string()
}

/// The most relevant trailing slice of a terraform log excerpt.
pub fn error_tail(excerpt: &str, max_lines: usize) -> String {
    let excerpt = ansi_strip(excerpt);
    let excerpt = trim_right(&excerpt, &['\n', ' ', '\t']);
    if excerpt.trim().is_empty() {
        return String::new();
    }
    let lines: Vec<&str> = excerpt.split('\n').collect();
    let start = lines.iter().rposition(|l| l.trim().starts_with('╷'));
    let picked: Vec<&str> = if let Some(start) = start {
        let end = lines[start..]
            .iter()
            .position(|l| l.trim().starts_with('╵'))
            .map_or(lines.len(), |j| start + j + 1);
        lines[start..end].to_vec()
    } else if let Some(err) = lines.iter().rposition(|l| l.contains("Error:")) {
        lines[err..].to_vec()
    } else {
        let mut picked: Vec<&str> = lines
            .iter()
            .rev()
            .filter(|l| !l.trim().is_empty())
            .take(max_lines)
            .copied()
            .collect();
        picked.reverse();
        picked
    };
    trim_right(&picked.join("\n"), &['\n'])
}

/// The actionable diagnosis of a failed stack.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct FailureTriage {
    pub class: &'static str,
    pub cause: &'static str,
    pub steps: &'static [&'static str],
    pub state_impact: &'static str,
    pub retryable: bool,
}

const FAILURE_MATCHERS: &[(&[&str], FailureTriage)] = &[
    (
        &[
            "cross-state move failed",
            "state_move_failed",
            ".tfsp-state-backups",
        ],
        FailureTriage {
            class: "state_move",
            cause: "A cross-state move failed partway. The source state was rolled back to its pre-move contents (or, if that failed, the error names the backup dir for manual restore).",
            steps: &[
                "Confirm the source/dest state from the error",
                "Re-run the apply once the move can complete",
                "If the error names .tfsp-state-backups, restore from there",
            ],
            state_impact: "Cross-state move — source restored from backup; re-run to complete (or restore manually from .tfsp-state-backups).",
            retryable: true,
        },
    ),
    (
        &[
            "quota exceeded",
            "quotaexceeded",
            "rate limit",
            "ratelimitexceeded",
        ],
        FailureTriage {
            class: "quota",
            cause: "A cloud quota or rate limit was hit.",
            steps: &[
                "Check the quota named in the error",
                "Request an increase or wait, then re-run",
            ],
            state_impact: "Partially applied up to the limit; re-run continues — review the plan before retrying.",
            retryable: false,
        },
    ),
    (
        &[
            "setiampolicy",
            "permission ",
            "error 403",
            "permissiondenied",
            "does not have permission",
            "iam.serviceaccount",
        ],
        FailureTriage {
            class: "iam_denied",
            cause: "The apply lacked an IAM permission — typically the PAM grant for this target isn't active (not yet approved, or expired mid-apply), so the leased service account can't make this write.",
            steps: &[
                "Re-request elevated access for the target",
                "Approve the grant, then re-run this tier's apply",
                "Inspect the full log for the exact permission",
            ],
            state_impact: "Safe to retry — denied writes don't land; re-running resumes from the unapplied changes once access is granted.",
            retryable: true,
        },
    ),
    (
        &[
            "acquiring the state lock",
            "state blob is already locked",
            "lock info",
        ],
        FailureTriage {
            class: "state_lock",
            cause: "Another run holds the Terraform state lock for this stack.",
            steps: &[
                "Wait for the other run to finish, then re-run",
                "If it's a stale lock, force-unlock per the runbook",
            ],
            state_impact: "Safe to retry once the lock clears — nothing was applied.",
            retryable: true,
        },
    ),
    (
        &["already exists", "error 409", "alreadyexists"],
        FailureTriage {
            class: "already_exists",
            cause: "A resource the plan creates already exists out-of-band (drift or a prior partial apply).",
            steps: &[
                "Import the existing resource into state, or remove it",
                "Re-plan to confirm, then re-run",
            ],
            state_impact: "Not auto-retryable — reconcile the conflict first.",
            retryable: false,
        },
    ),
    (
        &[
            "could not find default credentials",
            "oauth2",
            "invalid_grant",
            "unauthenticated",
            "error 401",
        ],
        FailureTriage {
            class: "provider_auth",
            cause: "The provider couldn't authenticate (token/credentials problem), not a per-resource permission.",
            steps: &[
                "Check the runner's credentials / token mint",
                "Re-run once auth is restored",
            ],
            state_impact: "Safe to retry — nothing was applied.",
            retryable: true,
        },
    ),
];

/// Maps a failed stack's error detail to a triage; unmatched errors get an empty
/// cause and the generic recovery.
pub fn classify_failure(detail: &str, _cats: &[Category]) -> FailureTriage {
    let d = detail.to_lowercase();
    for (needles, triage) in FAILURE_MATCHERS {
        if needles.iter().any(|n| d.contains(n)) {
            return triage.clone();
        }
    }
    FailureTriage {
        class: "error",
        cause: "",
        steps: &[
            "Read the error and the full log",
            "Fix forward and re-run this tier's apply",
        ],
        state_impact: "Review the log to determine whether any resources were partially applied before retrying.",
        retryable: true,
    }
}

/// A collapsible block per failed stack; "" when nothing failed.
pub fn failures_section(g: &Graph, log_url: &str, stack_log_prefix: &str) -> String {
    let failed: Vec<_> = g
        .stacks
        .iter()
        .filter(|s| s.status == Status::Failed)
        .collect();
    if failed.is_empty() {
        return String::new();
    }
    let mut b = format!("### ❌ Failures ({})\n\n", failed.len());
    if !log_url.is_empty() {
        let _ = write!(b, "🔗 [Full build log]({log_url})\n\n");
    }
    for s in failed {
        let _ = write!(b, "<details><summary><code>{}</code></summary>\n\n", s.path);
        if s.detail.is_empty() {
            b.push_str("_No error detail captured — see the build log._\n");
        } else {
            let _ = write!(b, "```\n{}\n```\n", s.detail);
            let tr = classify_failure(&s.detail, &s.categories);
            if !tr.cause.is_empty() {
                let _ = write!(b, "\n**Likely cause.** {}\n", tr.cause);
            }
            if !tr.steps.is_empty() {
                b.push_str("\n**Next steps:**\n\n");
                for (i, step) in tr.steps.iter().enumerate() {
                    let _ = writeln!(b, "{}. {step}", i + 1);
                }
            }
            if !tr.state_impact.is_empty() {
                let _ = write!(b, "\n**State impact.** {}\n", tr.state_impact);
            }
        }
        if !stack_log_prefix.is_empty() {
            let _ = write!(
                b,
                "\n📄 [Stack log]({}/{})\n",
                stack_log_prefix.trim_end_matches('/'),
                s.path
            );
        }
        b.push_str("\n</details>\n\n");
    }
    b.push_str("---\n\n");
    b
}

/// The DB-derived input to the verdict.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Snapshot {
    pub phase: Phase,
    pub total_stacks: usize,
    pub planned_stacks: usize,
    pub any_failed: bool,
    pub finalized: bool,
    pub total_gates: usize,
    pub active_gates: usize,
    pub denied_gates: usize,
}

impl Snapshot {
    /// Whether an unapproved (not rejected) gate is the only human dependency.
    pub fn awaiting_approval(&self) -> bool {
        !self.any_failed
            && self.denied_gates == 0
            && self.total_gates > 0
            && self.active_gates < self.total_gates
    }
}

/// The merge-lock evaluation of one PR: state is clear, held or unverifiable; the
/// default (empty state) means not evaluated.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct ApplyLockVerdict {
    pub state: String,
    pub blocking: Vec<String>,
    pub blocking_prs: Vec<i64>,
    pub reason: String,
}

/// Whether the merge lock blocks a green conclusion.
pub fn lock_blocked(v: &ApplyLockVerdict) -> bool {
    v.state == "held" || v.state == "unverifiable"
}

/// The check-run conclusion; "" leaves it in progress.
pub fn conclusion(s: &Snapshot, lock: &ApplyLockVerdict) -> &'static str {
    if s.any_failed {
        "failure"
    } else if s.denied_gates > 0 {
        "action_required"
    } else if (s.total_gates > 0 && s.active_gates < s.total_gates)
        || !s.finalized
        || lock_blocked(lock)
    {
        ""
    } else {
        "success"
    }
}

fn pr_refs(prs: &[i64]) -> Vec<String> {
    prs.iter().map(|p| format!("#{p}")).collect()
}

/// The check-run title when the merge lock is the only blocker.
pub fn lock_title(v: &ApplyLockVerdict) -> String {
    if v.state == "unverifiable" {
        return "merge-lock unverifiable — retrying".into();
    }
    let prs = pr_refs(&v.blocking_prs);
    match prs.len() {
        0 => "waiting on another PR's apply".into(),
        1 => format!("waiting on PR {}'s apply", prs[0]),
        _ => format!("waiting on PRs {} applies", prs.join(", ")),
    }
}

/// The merge-lock block of the consolidated check body; "" when clear.
pub fn lock_section(v: &ApplyLockVerdict) -> String {
    match v.state.as_str() {
        "held" => {
            let prs = pr_refs(&v.blocking_prs);
            let by = if prs.is_empty() {
                "another PR".to_string()
            } else {
                format!("PR {}", prs.join(", "))
            };
            format!(
                "## Merge lock\n\nHolding — stacks `{}` are being applied by {by}. Clears automatically when that apply finishes (or when its lease expires). Next: wait; if that apply is stuck, cancel/re-run it, an admin may bypass, or run `tfstackplan claims release`.\n\n",
                v.blocking.join("`, `")
            )
        }
        "unverifiable" => format!(
            "## Merge lock\n\nCan't verify the merge lock — {}. Retrying; re-run the plan if it failed.\n\n",
            v.reason
        ),
        _ => String::new(),
    }
}

/// The apply-lock check's title, summary and conclusion for a verdict.
pub fn apply_lock_output(v: &ApplyLockVerdict) -> (String, String, String) {
    match v.state.as_str() {
        "clear" => (
            "apply-lock: clear".into(),
            "No overlapping apply for this environment.".into(),
            "success".into(),
        ),
        "unverifiable" => (
            "apply-lock: can't verify".into(),
            format!(
                "Can't verify apply-lock — {}. Retrying; re-run the plan if it failed.",
                v.reason
            ),
            String::new(),
        ),
        _ => (
            "apply-lock: holding merge".into(),
            format!(
                "Holding — stacks `{}` are being applied by another PR. Clears automatically when that apply finishes (or when its lease expires). Next: wait; if that apply is stuck, cancel/re-run it, an admin may bypass, or run `tfstackplan claims release`.",
                v.blocking.join("`, `")
            ),
            String::new(),
        ),
    }
}

/// Counts for the progress fallback: (done, initialized, total), as the execution
/// read computes them.
pub fn count_stacks(stacks: &[StackState]) -> (usize, usize, usize) {
    let mut done_n = 0;
    let mut initialized = 0;
    for s in stacks {
        if matches!(
            s.status,
            Status::Initialized
                | Status::Planned
                | Status::Safe
                | Status::Nochange
                | Status::Moving
                | Status::Failed
                | Status::Aborted
                | Status::Gated
        ) {
            initialized += 1;
        }
        if matches!(
            s.status,
            Status::Planned
                | Status::Safe
                | Status::Nochange
                | Status::Failed
                | Status::Aborted
                | Status::Gated
                | Status::Moving
        ) {
            done_n += 1;
        }
    }
    (done_n, initialized, stacks.len())
}

/// The legacy plan check name: "plan/<env>".
pub fn check_run_name(environment: &str) -> String {
    status_context(environment)
}

/// The consolidated per-tier check: "terraform/<env>".
pub fn consolidated_check_name(environment: &str) -> String {
    if environment.is_empty() {
        "terraform".into()
    } else {
        format!("terraform/{environment}")
    }
}

/// The legacy merge-lock check: "apply-lock/<env>".
pub fn apply_lock_name(environment: &str) -> String {
    if environment.is_empty() {
        "apply-lock".into()
    } else {
        format!("apply-lock/{environment}")
    }
}

/// Whether a status context is a post-merge apply.
pub fn is_apply_context(ctx: &str) -> bool {
    ctx == "apply" || ctx.starts_with("apply/")
}

/// "plan/<env>", or "plan" without an environment.
pub fn status_context(environment: &str) -> String {
    if environment.is_empty() {
        "plan".into()
    } else {
        format!("plan/{environment}")
    }
}

/// Whether a run's context drives the plan gate.
pub fn is_gate(context: &str, environment: &str) -> bool {
    context.is_empty() || context == status_context(environment)
}

/// The execution context of a run kind.
pub fn run_context(kind: &str, environment: &str) -> String {
    if kind == tfstackplan_gate::reconcile::RUN_KIND_APPLY {
        if environment.is_empty() {
            "apply".into()
        } else {
            format!("apply/{environment}")
        }
    } else {
        status_context(environment)
    }
}

/// "apply" when the context starts with apply, else "plan".
pub fn exec_kind(context: &str) -> &'static str {
    if context.starts_with("apply") {
        "apply"
    } else {
        "plan"
    }
}

/// Whether an execution has concluded: a plan once its report arrived, an apply
/// once it has a terminal status.
pub fn is_finished(kind: &str, report: &str, status: &str) -> bool {
    if kind == "apply" {
        return matches!(
            status,
            "success" | "failure" | "action_required" | "cancelled" | "timed_out"
        );
    }
    !report.is_empty()
}

#[cfg(test)]
mod tests;
