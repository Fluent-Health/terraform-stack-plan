/**
 * prdata: pure reshaping of the tier-serve execution data into the PR-centric
 * views. No I/O — callers pass already-fetched summaries/details.
 */
import type { ExecutionSummary, StackState } from "./api/client";
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

export type ProjectGroup = { project: string; stacks: StackState[]; failed: boolean };

export function groupByProject(stacks: StackState[]): ProjectGroup[] {
  const order: string[] = [];
  const by = new Map<string, StackState[]>();
  for (const s of stacks) {
    const key = s.project || "(ungrouped)";
    if (!by.has(key)) { by.set(key, []); order.push(key); }
    by.get(key)!.push(s);
  }
  return order.map((project) => {
    const grp = by.get(project)!;
    return { project, stacks: grp, failed: grp.some((s) => statusSem(s.status ?? "") === "failed") };
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

export function distinctPRs(execs: ExecutionSummary[]): number[] {
  const newest = new Map<number, string>();
  for (const e of execs) {
    if (e.pr <= 0) continue;
    const cur = newest.get(e.pr);
    if (!cur || e.created_at > cur) newest.set(e.pr, e.created_at);
  }
  return [...newest.entries()].sort((a, b) => (a[1] < b[1] ? 1 : -1)).map(([pr]) => pr);
}
