import { describe, expect, it } from "vitest";
import { statusSem, SEM_DOT } from "./status";

describe("statusSem", () => {
  it("maps runner + serve statuses to the fixed semantic set", () => {
    expect(statusSem("safe")).toBe("ok");
    expect(statusSem("nochange")).toBe("ok");
    expect(statusSem("planned")).toBe("ok");
    expect(statusSem("applied")).toBe("ok");
    expect(statusSem("success")).toBe("ok");
    expect(statusSem("gated")).toBe("waiting");
    expect(statusSem("failed")).toBe("failed");
    expect(statusSem("aborted")).toBe("failed");
    expect(statusSem("failure")).toBe("failed");
    expect(statusSem("running")).toBe("running");
    expect(statusSem("initializing")).toBe("running");
    expect(statusSem("in_progress")).toBe("running");
    expect(statusSem("moving")).toBe("running");
    expect(statusSem("")).toBe("idle");
    expect(statusSem("pending")).toBe("idle");
    expect(statusSem("wat")).toBe("idle");
  });

  it("every semantic has a dot color", () => {
    for (const s of ["ok", "waiting", "failed", "running", "idle"] as const) {
      expect(SEM_DOT[s]).toMatch(/^#/);
    }
  });
});
