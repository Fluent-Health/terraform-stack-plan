/**
 * dag: pure helpers for the per-tier dependency graph. changeReasonFor turns a
 * stack's causality trigger into a human sentence; graphCounts summarises the
 * subgraph for the collapsed section header. No I/O, no DOM.
 */
import type { ExecutionDetail } from "./api/client";

type Reason = NonNullable<ExecutionDetail["change_reasons"]>[number];
type Graph = ExecutionDetail["graph"];

export function changeReasonFor(reasons: Reason[] | undefined, stack: string): string {
  const r = reasons?.find((x) => x.stack === stack);
  if (!r) return "";
  switch (r.kind) {
    case "watch":
      return `changed because watch ${r.via.join(", ")} changed`;
    case "module":
      return `changed via module ${r.via.join(", ")}`;
    case "direct":
      return "changed directly";
    default:
      return r.via.length ? `changed via ${r.via.join(", ")}` : "changed";
  }
}

export type GraphEdge = { from: string; to: string };

/**
 * normalizedEdges maps stored edge endpoints onto the graph's stack namespace
 * and drops edges touching stacks outside the set. Older runners recorded
 * run-graph edges project-root-relative (stacks/nonprod/cluster/x) while stack
 * paths are tier-relative (cluster/x) — rendered as-is every edge dangles and
 * the ELK layout fails. Exact match wins; otherwise an endpoint matches the
 * listed stack it path-suffixes ("…/" + stack).
 */
export function normalizedEdges(graph: Graph | undefined): GraphEdge[] {
  const stacks = (graph?.stacks ?? []).map((s) => s.path);
  const exact = new Set(stacks);
  const resolve = (endpoint: string): string | undefined => {
    if (exact.has(endpoint)) return endpoint;
    return stacks.find((s) => endpoint.endsWith("/" + s));
  };
  const out: GraphEdge[] = [];
  for (const e of graph?.edges ?? []) {
    const from = resolve(e.from);
    const to = resolve(e.to);
    if (from !== undefined && to !== undefined) out.push({ from, to });
  }
  return out;
}

export function graphCounts(graph: Graph | undefined): { stacks: number; edges: number } {
  return { stacks: graph?.stacks?.length ?? 0, edges: normalizedEdges(graph).length };
}
