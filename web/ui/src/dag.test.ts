import { describe, expect, it } from "vitest";
import { changeReasonFor, graphCounts, normalizedEdges } from "./dag";
import type { ExecutionDetail } from "./api/client";

type Reason = NonNullable<ExecutionDetail["change_reasons"]>[number];

describe("changeReasonFor", () => {
  const reasons: Reason[] = [
    { stack: "projects/a", kind: "watch", via: ["modules/am/**"] },
    { stack: "projects/b", kind: "direct", via: [] },
  ];
  it("describes a watch trigger with its glob", () => {
    expect(changeReasonFor(reasons, "projects/a")).toBe("changed because watch modules/am/** changed");
  });
  it("describes a direct change", () => {
    expect(changeReasonFor(reasons, "projects/b")).toBe("changed directly");
  });
  it("returns empty for an unknown stack", () => {
    expect(changeReasonFor(reasons, "projects/z")).toBe("");
  });
  it("tolerates undefined reasons", () => {
    expect(changeReasonFor(undefined, "projects/a")).toBe("");
  });
});

describe("graphCounts", () => {
  it("counts stacks and edges", () => {
    expect(graphCounts({ stacks: [{ path: "a" }, { path: "b" }], edges: [{ from: "a", to: "b" }] } as any))
      .toEqual({ stacks: 2, edges: 1 });
  });
  it("tolerates a missing graph", () => {
    expect(graphCounts(undefined)).toEqual({ stacks: 0, edges: 0 });
  });
  it("counts only renderable (normalized) edges", () => {
    // Stored executions may carry project-root-relative edge endpoints while
    // stacks are tier-relative — the header count must match what the graph
    // can actually draw.
    const g = {
      stacks: [{ path: "cluster/x" }, { path: "projects/x" }],
      edges: [
        { from: "stacks/nonprod/projects/x", to: "stacks/nonprod/cluster/x" },
        { from: "stacks/nonprod/sql/x", to: "stacks/nonprod/cluster/x" },
      ],
    } as any;
    expect(graphCounts(g)).toEqual({ stacks: 2, edges: 1 });
  });
});

describe("normalizedEdges", () => {
  const stacks = [{ path: "cluster/x" }, { path: "workloads/agent/x" }] as any[];
  it("maps prefix-qualified endpoints onto the stack namespace", () => {
    expect(
      normalizedEdges(
        { stacks, edges: [{ from: "stacks/nonprod/cluster/x", to: "stacks/nonprod/workloads/agent/x" }] } as any,
      ),
    ).toEqual([{ from: "cluster/x", to: "workloads/agent/x" }]);
  });
  it("keeps already-matching endpoints as-is", () => {
    expect(normalizedEdges({ stacks, edges: [{ from: "cluster/x", to: "workloads/agent/x" }] } as any))
      .toEqual([{ from: "cluster/x", to: "workloads/agent/x" }]);
  });
  it("drops edges touching stacks outside the changed set", () => {
    expect(
      normalizedEdges({ stacks, edges: [{ from: "stacks/nonprod/sql/x", to: "stacks/nonprod/cluster/x" }] } as any),
    ).toEqual([]);
  });
  it("prefers an exact match over a suffix match", () => {
    const g = {
      stacks: [{ path: "agent/x" }, { path: "workloads/agent/x" }],
      edges: [{ from: "agent/x", to: "workloads/agent/x" }],
    } as any;
    expect(normalizedEdges(g)).toEqual([{ from: "agent/x", to: "workloads/agent/x" }]);
  });
  it("tolerates a missing graph", () => {
    expect(normalizedEdges(undefined)).toEqual([]);
  });
});
