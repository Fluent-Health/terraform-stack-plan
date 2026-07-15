import { describe, expect, it } from "vitest";
import { stepperSegments, nowCaption, tooltipText, stageFromLifecycle } from "./stepper";
import type { LifecyclePhase } from "./api/client";

const ph = (o: Partial<LifecyclePhase>): LifecyclePhase =>
  ({ key: "", label: "", state: "pending", ...o }) as LifecyclePhase;

describe("stepperSegments", () => {
  it("maps phase state to a semantic colour class, preserving order", () => {
    const segs = stepperSegments([
      ph({ key: "plan", label: "plan", state: "done" }),
      ph({ key: "report", label: "report", state: "now" }),
      ph({ key: "apply", label: "apply", state: "pending", context: "apply" }),
      ph({ key: "verify", label: "verify", state: "failed" }),
    ]);
    expect(segs.map((s) => s.state)).toEqual(["done", "now", "pending", "failed"]);
    expect(segs[0].cls).toContain("success");
    expect(segs[1].cls).toContain("info");
    expect(segs[2].cls).toContain("base-300");
    expect(segs[3].cls).toContain("error");
  });

  it("marks the gate/apply boundary before the first gate-side segment", () => {
    const segs = stepperSegments([
      ph({ key: "plan", context: "plan", state: "done" }),
      ph({ key: "approve", context: "gate", state: "now" }),
      ph({ key: "apply", context: "apply", state: "pending" }),
    ]);
    expect(segs[0].divider).toBe(false);
    expect(segs[1].divider).toBe(true); // ┆ before the gate/apply side
    expect(segs[2].divider).toBe(false);
  });

  it("keeps passthrough phases (linting) on the plan side of the divider", () => {
    const segs = stepperSegments([
      ph({ key: "prepare", context: "plan", state: "done" }),
      ph({ key: "linting", context: "plan", state: "done" }),
      ph({ key: "plan", context: "plan", state: "now" }),
      ph({ key: "approve", context: "gate", state: "pending" }),
    ]);
    expect(segs.map((s) => s.divider)).toEqual([false, false, false, true]);
  });

  it("renders no divider on a plan-only bar", () => {
    const segs = stepperSegments([
      ph({ key: "prepare", context: "plan", state: "done" }),
      ph({ key: "plan", context: "plan", state: "now" }),
      ph({ key: "report", context: "plan", state: "pending" }),
    ]);
    expect(segs.every((s) => !s.divider)).toBe(true);
  });

  it("carries within-segment fill only on the now segment", () => {
    const segs = stepperSegments([
      ph({ key: "plan", state: "now", progress_pct: 40 }),
      ph({ key: "report", state: "pending" }),
    ]);
    expect(segs[0].fillPct).toBe(40);
    expect(segs[1].fillPct).toBeUndefined();
  });
});

describe("nowCaption", () => {
  it("summarises the current phase with its plain-language description", () => {
    expect(nowCaption([ph({ key: "report", label: "report", state: "now", result: "+2 ~1" })])).toBe(
      "report — rendering the reviewer report · +2 ~1",
    );
  });
  it("includes the sub-phase detail and progress when present", () => {
    expect(
      nowCaption([ph({ key: "apply", label: "apply", state: "now", detail: "warming caches", progress_pct: 25 })]),
    ).toBe("apply — applying the planned changes to real infrastructure · warming caches · 25%");
  });
  it("is empty when nothing is running", () => {
    expect(nowCaption([ph({ state: "done" }), ph({ state: "pending" })])).toBe("");
  });
});

describe("tooltipText", () => {
  it("past shows ✓ name + description + result", () => {
    expect(tooltipText(ph({ key: "plan", label: "plan", state: "done", result: "+2 ~1" }))).toBe(
      "✓ plan — computing what would change in each stack · +2 ~1",
    );
  });
  it("now shows ▸ name running with description", () => {
    expect(tooltipText(ph({ key: "apply", label: "apply", state: "now" }))).toBe(
      "▸ apply · running — applying the planned changes to real infrastructure",
    );
  });
  it("pending shows ○ name + wait reason", () => {
    expect(tooltipText(ph({ key: "approve", label: "approve", state: "pending", result: "waits on approval" }))).toBe(
      "○ approve · pending — waiting for a human to approve the gated changes · waits on approval",
    );
  });
  it("unknown keys degrade to the bare state line", () => {
    expect(tooltipText(ph({ key: "custom", label: "custom", state: "done" }))).toBe("✓ custom");
  });
});

describe("stageFromLifecycle", () => {
  it("planning while a plan-side phase runs", () => {
    expect(stageFromLifecycle([ph({ key: "plan", state: "now" }), ph({ key: "report", state: "pending" })])).toBe(
      "planning",
    );
  });
  it("planned when the plan side is fully done", () => {
    expect(stageFromLifecycle([ph({ key: "plan", state: "done" }), ph({ key: "report", state: "done" })])).toBe(
      "planned",
    );
  });
  it("awaiting approval on a pending approve gate", () => {
    expect(
      stageFromLifecycle([ph({ key: "report", state: "done" }), ph({ key: "approve", state: "pending" })]),
    ).toBe("awaiting approval");
  });
  it("applying while the apply (or moves) segment runs", () => {
    expect(stageFromLifecycle([ph({ key: "report", state: "done" }), ph({ key: "apply", state: "now" })])).toBe(
      "applying",
    );
    expect(stageFromLifecycle([ph({ key: "report", state: "done" }), ph({ key: "moves", state: "now" })])).toBe(
      "applying",
    );
  });
  it("applied when the apply segment is done", () => {
    expect(stageFromLifecycle([ph({ key: "report", state: "done" }), ph({ key: "apply", state: "done" })])).toBe(
      "applied",
    );
  });
  it("does not claim applying when the apply segment is only pending (queued, not running)", () => {
    // Plan done, approved, apply registered but not yet started (pending). The
    // badge must not read "applying" — nothing is applying yet.
    expect(
      stageFromLifecycle([
        ph({ key: "report", state: "done" }),
        ph({ key: "approve", state: "done" }),
        ph({ key: "apply", state: "pending", context: "apply" }),
      ]),
    ).not.toBe("applying");
  });
  it("failed wins over everything", () => {
    expect(stageFromLifecycle([ph({ key: "plan", state: "failed" }), ph({ key: "apply", state: "pending" })])).toBe(
      "failed",
    );
  });
  it("empty input yields no stage", () => {
    expect(stageFromLifecycle([])).toBe("");
  });
});
