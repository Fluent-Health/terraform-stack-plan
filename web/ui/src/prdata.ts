/**
 * prdata: pure reshaping of the tier-serve execution data into the PR-centric
 * views. No I/O — callers pass already-fetched summaries/details.
 */
import type { ExecutionSummary, StackState } from "./api/client";
import { statusSem } from "./status";

export function latestPerTier(execs: ExecutionSummary[]): ExecutionSummary[] {
  const best = new Map<string, ExecutionSummary>();
  for (const e of execs) {
    if (e.superseded_by) continue;
    const cur = best.get(e.context);
    if (!cur || e.created_at > cur.created_at) best.set(e.context, e);
  }
  return [...best.values()];
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

export function distinctPRs(execs: ExecutionSummary[]): number[] {
  const newest = new Map<number, string>();
  for (const e of execs) {
    if (e.pr <= 0) continue;
    const cur = newest.get(e.pr);
    if (!cur || e.created_at > cur) newest.set(e.pr, e.created_at);
  }
  return [...newest.entries()].sort((a, b) => (a[1] < b[1] ? 1 : -1)).map(([pr]) => pr);
}
