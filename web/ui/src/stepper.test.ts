import { describe, expect, it } from "vitest";
import { STAGES, stageStates } from "./stepper";

describe("stageStates", () => {
  it("marks stages before the current phase done, the current now, later pending", () => {
    const st = stageStates("apply", "in_progress");
    const iApply = STAGES.indexOf("apply");
    expect(st[iApply]).toBe("now");
    expect(st[iApply - 1]).toBe("done");
    expect(st[STAGES.length - 1]).toBe("pending");
  });

  it("a failed execution paints the current phase failed, not now", () => {
    const st = stageStates("apply", "failure");
    expect(st[STAGES.indexOf("apply")]).toBe("failed");
  });

  it("a finished-clean execution marks every stage done", () => {
    const st = stageStates("verify", "success");
    expect(st.every((s) => s === "done")).toBe(true);
  });

  it("an unknown phase leaves everything pending (never throws)", () => {
    const st = stageStates("wat", "in_progress");
    expect(st.every((s) => s === "pending")).toBe(true);
    expect(st).toHaveLength(STAGES.length);
  });
});
