import { describe, expect, it } from "vitest";
import {
  contextKind,
  primaryExec,
  latestPerContext,
  rollupSem,
  prLifecycleStage,
  groupByProject,
  distinctPRs,
  progressCounts,
  approvalsByTarget,
  relativeTime,
  rollupChangeCounts,
  mergeBadge,
  sortedQueueEntries,
} from "./prdata";
import type { ExecutionSummary, PendingApproval, StackState, PRView } from "./api/client";

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

describe("prLifecycleStage", () => {
  const noApprovals: PendingApproval[] = [];
  const pa = (o: Partial<PendingApproval>): PendingApproval =>
    ({ pr: 0, environment: "", repo: "", class: "", target: "", grant_name: "", state: "", requester: "", ...o }) as PendingApproval;

  it("applying wins over a stale in_progress plan (terminal/later-phase precedence)", () => {
    expect(prLifecycleStage([
      ex({ context: "plan/prod", status: "in_progress", created_at: "2026-07-12T01:00:00Z" }),
      ex({ context: "apply/prod", status: "in_progress", created_at: "2026-07-12T03:00:00Z" }),
    ], noApprovals)).toBe("applying");
  });

  it("applied wins over a stale in_progress plan", () => {
    expect(prLifecycleStage([
      ex({ context: "plan/prod", status: "in_progress", created_at: "2026-07-12T01:00:00Z" }),
      ex({ context: "apply/prod", status: "applied", created_at: "2026-07-12T03:00:00Z" }),
    ], noApprovals)).toBe("applied");
  });

  it("failed apply is terminal", () => {
    expect(prLifecycleStage([
      ex({ context: "apply/prod", status: "failed", created_at: "2026-07-12T03:00:00Z" }),
    ], noApprovals)).toBe("failed");
  });

  it("planned then pending approval reads as awaiting-approval", () => {
    expect(prLifecycleStage([
      ex({ context: "plan/prod", status: "planned", created_at: "2026-07-12T01:00:00Z" }),
    ], [pa({ pr: 0, target: "proj-a" })])).toBe("awaiting-approval");
  });

  it("plan success without a gate reads as planned", () => {
    expect(prLifecycleStage([
      ex({ context: "plan/prod", status: "planned", created_at: "2026-07-12T01:00:00Z" }),
    ], noApprovals)).toBe("planned");
  });

  it("plan in_progress reads as planning", () => {
    expect(prLifecycleStage([
      ex({ context: "plan/prod", status: "in_progress", created_at: "2026-07-12T01:00:00Z" }),
    ], noApprovals)).toBe("planning");
  });

  it("ignores superseded executions and empties to idle", () => {
    expect(prLifecycleStage([
      ex({ context: "apply/prod", status: "failed", superseded_by: "x", created_at: "2026-07-12T03:00:00Z" }),
    ], noApprovals)).toBe("idle");
    expect(prLifecycleStage([], noApprovals)).toBe("idle");
  });
});

describe("relativeTime", () => {
  const now = new Date("2026-07-12T12:00:00Z");
  it("renders coarse buckets", () => {
    expect(relativeTime("2026-07-12T11:59:50Z", now)).toBe("just now");
    expect(relativeTime("2026-07-12T11:59:15Z", now)).toBe("just now"); // 45s → still "just now"
    expect(relativeTime("2026-07-12T11:55:00Z", now)).toBe("5m ago");
    expect(relativeTime("2026-07-12T09:00:00Z", now)).toBe("3h ago");
    expect(relativeTime("2026-07-10T12:00:00Z", now)).toBe("2d ago");
  });
  it("returns empty for empty/invalid input", () => {
    expect(relativeTime("", now)).toBe("");
    expect(relativeTime("not-a-date", now)).toBe("");
  });
});

describe("rollupChangeCounts", () => {
  it("rolls up per-kind counts across stacks", () => {
    const s = (counts: Record<string, number>): StackState => ({ path: "p", counts } as StackState);
    expect(rollupChangeCounts([s({ add: 1, change: 2 }), s({ add: 2, destroy: 1 })])).toBe("+3 ~2 −1");
  });
  it("is empty when nothing changed", () => {
    expect(rollupChangeCounts([{ path: "p" } as StackState])).toBe("");
  });
  it("includes replace and move glyphs", () => {
    const s = (counts: Record<string, number>): StackState => ({ path: "p", counts } as StackState);
    expect(rollupChangeCounts([s({ replace: 2, move: 3 })])).toBe("±2 ↔3");
  });
});

describe("mergeBadge", () => {
  const view = (o: Partial<PRView>): PRView =>
    ({ pr: 1, repo: "o/r", merge: { environment: "", required_check: "", check_conclusion: "", merge_blocked: false, blocker: "" }, ...o } as PRView);
  it("undefined for no view", () => expect(mergeBadge(undefined)).toBeUndefined());
  it("blocked wins with amber", () => {
    const b = mergeBadge(view({ merge: { environment: "", required_check: "", check_conclusion: "", merge_blocked: true, blocker: "waits on prod approval" } }))!;
    expect(b).toEqual({ label: "waits on prod approval", sem: "waiting" });
  });
  it("automerge on when not blocked", () => {
    const b = mergeBadge(view({ meta: { title: "t", body: "", author_login: "a", head_ref: "h", url: "", auto_merge: true } }))!;
    expect(b).toEqual({ label: "automerge on", sem: "ok" });
  });
  it("mergeable when the check passed", () => {
    const b = mergeBadge(view({ merge: { environment: "", required_check: "terraform/prod", check_conclusion: "success", merge_blocked: false, blocker: "" } }))!;
    expect(b).toEqual({ label: "mergeable", sem: "ok" });
  });
  it("open by default", () => {
    expect(mergeBadge(view({}))).toEqual({ label: "open", sem: "idle" });
  });
  it("blocked wins over automerge when both are set", () => {
    const b = mergeBadge(view({
      merge: { environment: "", required_check: "", check_conclusion: "", merge_blocked: true, blocker: "waits on prod approval" },
      meta: { title: "t", body: "", author_login: "a", head_ref: "h", url: "", auto_merge: true },
    }))!;
    expect(b).toEqual({ label: "waits on prod approval", sem: "waiting" });
  });
});

describe("sortedQueueEntries", () => {
  it("orders ascending by position", () => {
    expect(sortedQueueEntries([{ position: 3 }, { position: 1 }, { position: 2 }]).map((e) => e.position)).toEqual([1, 2, 3]);
  });
});
