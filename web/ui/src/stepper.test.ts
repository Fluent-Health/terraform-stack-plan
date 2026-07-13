import { describe, expect, it } from "vitest";
import { stepperSegments, nowCaption, tooltipText } from "./stepper";
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

  it("marks the gate/apply boundary before the first non-plan-side segment", () => {
    const segs = stepperSegments([
      ph({ key: "plan", context: "plan", state: "done" }),
      ph({ key: "approve", context: "gate", state: "now" }),
      ph({ key: "apply", context: "apply", state: "pending" }),
    ]);
    expect(segs[0].divider).toBe(false);
    expect(segs[1].divider).toBe(true); // ┆ before the gate/apply side
    expect(segs[2].divider).toBe(false);
  });
});

describe("nowCaption", () => {
  it("summarises the current (now) phase", () => {
    expect(nowCaption([ph({ key: "report", label: "report", state: "now", result: "+2 ~1" })])).toBe(
      "report · +2 ~1",
    );
  });
  it("is empty when nothing is running", () => {
    expect(nowCaption([ph({ state: "done" }), ph({ state: "pending" })])).toBe("");
  });
});

describe("tooltipText", () => {
  it("past shows ✓ name + result", () => {
    expect(tooltipText(ph({ label: "plan", state: "done", result: "+2 ~1" }))).toBe("✓ plan · +2 ~1");
  });
  it("now shows ▸ name running", () => {
    expect(tooltipText(ph({ label: "apply", state: "now" }))).toBe("▸ apply · running");
  });
  it("pending shows ○ name + wait reason", () => {
    expect(tooltipText(ph({ label: "apply", state: "pending", result: "waits on approval" }))).toBe(
      "○ apply · pending · waits on approval",
    );
  });
});
