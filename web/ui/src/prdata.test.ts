import { describe, expect, it } from "vitest";
import {
  contextKind,
  primaryExec,
  latestPerContext,
  rollupSem,
  groupByProject,
  distinctPRs,
  progressCounts,
  approvalsByTarget,
} from "./prdata";
import type { ExecutionSummary, PendingApproval, StackState } from "./api/client";

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
    expect(g[0].project).toBe("Global / Untagged Stacks");
  });
});

describe("progressCounts", () => {
  it("tallies done/running/failed/total by semantic", () => {
    const st = (status: string): StackState => ({ path: "p", status } as StackState);
    const c = progressCounts([st("safe"), st("planned"), st("failed"), st("running"), st("pending")]);
    expect(c).toEqual({ done: 2, running: 1, failed: 1, total: 5 });
  });
});

describe("approvalsByTarget", () => {
  const pa = (o: Partial<PendingApproval>): PendingApproval =>
    ({ pr: 0, environment: "", repo: "", class: "", target: "", grant_name: "", state: "", requester: "", ...o }) as PendingApproval;

  it("indexes by target, filtered to the given PR", () => {
    const m = approvalsByTarget(
      [
        pa({ pr: 1, target: "proj-a", class: "sensitive-project" }),
        pa({ pr: 2, target: "proj-b", class: "sensitive-project" }),
      ],
      1,
    );
    expect(m.size).toBe(1);
    expect(m.get("proj-a")?.class).toBe("sensitive-project");
    expect(m.get("proj-b")).toBeUndefined();
  });

  it("returns an empty map when nothing is pending for this PR", () => {
    expect(approvalsByTarget([pa({ pr: 9, target: "proj-a" })], 1).size).toBe(0);
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
