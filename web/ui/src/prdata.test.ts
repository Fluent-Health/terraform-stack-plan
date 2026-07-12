import { describe, expect, it } from "vitest";
import { contextKind, primaryExec, latestPerContext, rollupSem, groupByProject, distinctPRs } from "./prdata";
import type { ExecutionSummary, StackState } from "./api/client";

const ex = (o: Partial<ExecutionSummary>): ExecutionSummary =>
  ({ id: "", pr: 0, context: "", phase: "", status: "", superseded_by: "",
     created_at: "2026-07-12T00:00:00Z", sha: "", log_url: "", ...o } as ExecutionSummary);

describe("latestPerContext", () => {
  it("keeps the newest non-superseded execution per context", () => {
    const out = latestPerContext([
      ex({ id: "a", context: "terraform/prod", created_at: "2026-07-12T01:00:00Z" }),
      ex({ id: "b", context: "terraform/prod", created_at: "2026-07-12T02:00:00Z" }),
      ex({ id: "c", context: "terraform/nonprod", created_at: "2026-07-12T01:30:00Z" }),
    ]);
    expect(out.map((e) => e.id).sort()).toEqual(["b", "c"]);
  });
});

describe("contextKind", () => {
  it("parses the context head; terraform is the gate", () => {
    expect(contextKind("plan/nonprod")).toBe("plan");
    expect(contextKind("apply/prod")).toBe("apply");
    expect(contextKind("verify/nonprod")).toBe("verify");
    expect(contextKind("terraform/prod")).toBe("gate");
    expect(contextKind("weird")).toBe("other");
  });
});

describe("primaryExec", () => {
  it("returns the newest non-superseded execution", () => {
    const got = primaryExec([
      ex({ id: "old", created_at: "2026-07-12T01:00:00Z" }),
      ex({ id: "new", created_at: "2026-07-12T03:00:00Z" }),
      ex({ id: "sup", created_at: "2026-07-12T04:00:00Z", superseded_by: "x" }),
    ]);
    expect(got?.id).toBe("new");
  });
  it("returns undefined when all superseded/empty", () => {
    expect(primaryExec([])).toBeUndefined();
  });
});

describe("rollupSem", () => {
  it("takes the worst live semantic", () => {
    expect(rollupSem([ex({ status: "safe" }), ex({ status: "failed" }), ex({ status: "running" })])).toBe("failed");
    expect(rollupSem([ex({ status: "safe" }), ex({ status: "running" })])).toBe("running");
    expect(rollupSem([ex({ status: "failed", superseded_by: "y" }), ex({ status: "safe" })])).toBe("ok");
    expect(rollupSem([])).toBe("idle");
  });
});

describe("groupByProject", () => {
  it("groups stacks by project in first-seen order and flags failures", () => {
    const stacks: StackState[] = [
      { path: "gke/", project: "p1", status: "safe" } as StackState,
      { path: "redis/", project: "p2", status: "failed" } as StackState,
      { path: "sql/", project: "p1", status: "safe" } as StackState,
    ];
    const g = groupByProject(stacks);
    expect(g.map((x) => x.project)).toEqual(["p1", "p2"]);
    expect(g[0].stacks).toHaveLength(2);
    expect(g[1].failed).toBe(true);
    expect(g[0].failed).toBe(false);
  });

  it("uses (ungrouped) for empty project", () => {
    const g = groupByProject([{ path: "x/", project: "", status: "safe" } as StackState]);
    expect(g[0].project).toBe("(ungrouped)");
  });
});

describe("distinctPRs", () => {
  it("returns PR numbers >0 newest-first", () => {
    expect(distinctPRs([
      ex({ pr: 5, created_at: "2026-07-12T01:00:00Z" }),
      ex({ pr: 7, created_at: "2026-07-12T03:00:00Z" }),
      ex({ pr: 5, created_at: "2026-07-12T02:00:00Z" }),
      ex({ pr: 0 }),
    ])).toEqual([7, 5]);
  });
});
