/**
 * prdata: pure reshaping of the tier-serve execution data into the PR-centric
 * views. No I/O — callers pass already-fetched summaries/details.
 */
import type { ExecutionSummary, PendingApproval, StackState, PRView } from "./api/client";
import { statusSem, type Sem } from "./status";

export type ContextKind = "plan" | "apply" | "verify" | "gate" | "other";
export function contextKind(context: string): ContextKind {
  const head = context.split("/")[0];
  if (head === "plan" || head === "apply" || head === "verify") return head;
  if (head === "terraform") return "gate";
  return "other";
}

// Newest non-superseded execution per full context (one entry per plan/apply/verify/gate context, not per tier).
export function latestPerContext(execs: ExecutionSummary[]): ExecutionSummary[] {
  const best = new Map<string, ExecutionSummary>();
  for (const e of execs) {
    if (e.superseded_by) continue;
    const cur = best.get(e.context);
    if (!cur || e.created_at > cur.created_at) best.set(e.context, e);
  }
  return [...best.values()];
}

export function primaryExec(execs: ExecutionSummary[]): ExecutionSummary | undefined {
  let best: ExecutionSummary | undefined;
  for (const e of execs) {
    if (e.superseded_by) continue;
    if (!best || e.created_at > best.created_at) best = e;
  }
  return best;
}

const SEM_RANK: Record<Sem, number> = { failed: 4, running: 3, waiting: 2, ok: 1, idle: 0 };
export function rollupSem(execs: ExecutionSummary[]): Sem {
  let worst: Sem = "idle";
  for (const e of execs) {
    if (e.superseded_by) continue;
    const s = statusSem(e.status);
    if (SEM_RANK[s] > SEM_RANK[worst]) worst = s;
  }
  return worst;
}

export type PrStage =
  | "planning"
  | "planned"
  | "awaiting-approval"
  | "applying"
  | "applied"
  | "failed"
  | "idle";

// prLifecycleStage derives one tier's PR lifecycle stage from its executions +
// pending approvals. Terminal / later-phase state wins: the newest apply-or-
// verify execution is consulted before the plan, so a stale in_progress plan
// never masks an applied result. See the precedence table in the plan.
export function prLifecycleStage(execs: ExecutionSummary[], approvals: PendingApproval[]): PrStage {
  const live = execs.filter((e) => !e.superseded_by);
  const newestOf = (...kinds: ContextKind[]): ExecutionSummary | undefined => {
    let best: ExecutionSummary | undefined;
    for (const e of live) {
      if (!kinds.includes(contextKind(e.context))) continue;
      if (!best || e.created_at > best.created_at) best = e;
    }
    return best;
  };
  const applyOrVerify = newestOf("apply", "verify");
  if (applyOrVerify) {
    const s = statusSem(applyOrVerify.status);
    if (s === "failed") return "failed";
    if (s === "running") return "applying";
    if (s === "ok") return "applied";
    if (s === "waiting") return "awaiting-approval";
  }
  if (approvals.length > 0) return "awaiting-approval";
  const plan = newestOf("plan");
  if (plan) {
    const s = statusSem(plan.status);
    if (s === "failed") return "failed";
    if (s === "ok") return "planned";
    return "planning";
  }
  return "idle";
}

const STAGE_SEM: Record<PrStage, Sem> = {
  planning: "running",
  planned: "ok",
  "awaiting-approval": "waiting",
  applying: "running",
  applied: "ok",
  failed: "failed",
  idle: "idle",
};
export function stageSem(stage: PrStage): Sem {
  return STAGE_SEM[stage];
}
export const STAGE_LABEL: Record<PrStage, string> = {
  planning: "planning",
  planned: "planned",
  "awaiting-approval": "awaiting approval",
  applying: "applying",
  applied: "applied",
  failed: "failed",
  idle: "idle",
};

export type ComponentGroup = { component: string; stacks: StackState[]; failed: boolean };

// groupByComponent keys stacks by their path-derived component: the stack path
// with its leaf segment dropped (projects/fh-dev-svc → projects;
// workloads/backend/fh-dev-svc → workloads/backend). Always present — no
// "untagged" fallback. A leafless path keys under itself.
export function groupByComponent(stacks: StackState[]): ComponentGroup[] {
  const order: string[] = [];
  const by = new Map<string, StackState[]>();
  for (const s of stacks) {
    const trimmed = s.path.replace(/\/+$/, "");
    const slash = trimmed.lastIndexOf("/");
    const key = slash > 0 ? trimmed.slice(0, slash) : trimmed;
    if (!by.has(key)) { by.set(key, []); order.push(key); }
    by.get(key)!.push(s);
  }
  return order.map((component) => {
    const grp = by.get(component)!;
    return { component, stacks: grp, failed: grp.some((s) => statusSem(s.status ?? "") === "failed") };
  });
}

export function progressCounts(stacks: StackState[]): { done: number; running: number; failed: number; total: number } {
  let done = 0, running = 0, failed = 0;
  for (const s of stacks) {
    const sem = statusSem(s.status ?? "");
    if (sem === "ok") done++;
    else if (sem === "running") running++;
    else if (sem === "failed") failed++;
  }
  return { done, running, failed, total: stacks.length };
}

// Index a tier's pending approvals (already PR-unfiltered from the API) by
// gate target for this one PR, so the gates strip can enumerate every pending
// approval (keyed by target) independent of how changes are grouped.
export function approvalsByTarget(approvals: PendingApproval[], pr: number): Map<string, PendingApproval> {
  const m = new Map<string, PendingApproval>();
  for (const a of approvals) {
    if (a.pr === pr) m.set(a.target, a);
  }
  return m;
}

export function distinctPRs(execs: ExecutionSummary[]): number[] {
  const newest = new Map<number, string>();
  for (const e of execs) {
    if (e.pr <= 0) continue;
    const cur = newest.get(e.pr);
    if (!cur || e.created_at > cur) newest.set(e.pr, e.created_at);
  }
  return [...newest.entries()].sort((a, b) => (a[1] < b[1] ? 1 : -1)).map(([pr]) => pr);
}

// relativeTime renders a compact "Xm/Xh/Xd ago" for the PRs list; beyond ~30d
// it falls back to a locale date. Empty/invalid input renders nothing.
export function relativeTime(iso: string, now: Date = new Date()): string {
  if (!iso) return "";
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return "";
  const secs = Math.max(0, Math.floor((now.getTime() - t) / 1000));
  if (secs < 60) return "just now";
  const mins = Math.floor(secs / 60);
  if (mins < 60) return `${mins}m ago`;
  const hours = Math.floor(mins / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.floor(hours / 24);
  if (days <= 30) return `${days}d ago`;
  return new Date(t).toLocaleDateString();
}

// rollupChangeCounts sums per-kind operation counts across stacks into the
// reviewer glyph label "+a ~b ±c −d ↔e" (same vocabulary as the tier panel).
export function rollupChangeCounts(stacks: StackState[]): string {
  let add = 0, change = 0, replace = 0, destroy = 0, move = 0;
  for (const s of stacks) {
    const c = s.counts;
    if (!c) continue;
    add += c.add ?? 0;
    change += c.change ?? 0;
    replace += c.replace ?? 0;
    destroy += c.destroy ?? 0;
    move += c.move ?? 0;
  }
  const p: string[] = [];
  if (add) p.push(`+${add}`);
  if (change) p.push(`~${change}`);
  if (replace) p.push(`±${replace}`);
  if (destroy) p.push(`−${destroy}`);
  if (move) p.push(`↔${move}`);
  return p.join(" ");
}

// mergeBadge derives the row's merge/automerge chip from a tier's PRView.
export function mergeBadge(view: PRView | undefined): { label: string; sem: Sem } | undefined {
  if (!view) return undefined;
  if (view.merge?.merge_blocked) return { label: view.merge.blocker || "merge blocked", sem: "waiting" };
  if (view.meta?.auto_merge) return { label: "automerge on", sem: "ok" };
  if (statusSem(view.merge?.check_conclusion ?? "") === "ok") return { label: "mergeable", sem: "ok" };
  return { label: "open", sem: "idle" };
}

// sortedQueueEntries orders merge-queue entries by position ascending.
export function sortedQueueEntries<T extends { position: number }>(entries: T[]): T[] {
  return [...entries].sort((a, b) => a.position - b.position);
}
