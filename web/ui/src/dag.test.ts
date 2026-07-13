import { describe, expect, it } from "vitest";
import { changeReasonFor, graphCounts } from "./dag";
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
});
