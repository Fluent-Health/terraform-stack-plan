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

export function graphCounts(graph: Graph | undefined): { stacks: number; edges: number } {
  return { stacks: graph?.stacks?.length ?? 0, edges: graph?.edges?.length ?? 0 };
}
