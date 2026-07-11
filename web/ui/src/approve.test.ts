import { describe, expect, it } from "vitest";
import { approveURL, isApprovalOutcome } from "./approve";

describe("approveURL", () => {
  it("builds the consent entry with encoded params", () => {
    const grant = "projects/p/locations/global/entitlements/iam/grants/g1";
    const url = approveURL("prod", grant, "approve", "looks good");
    expect(url).toBe(
      "/auth/approve?tier=prod&grant=projects%2Fp%2Flocations%2Fglobal%2Fentitlements%2Fiam%2Fgrants%2Fg1&decision=approve&reason=looks+good",
    );
  });
  it("omits an empty reason", () => {
    expect(approveURL("prod", "g", "deny", "")).not.toContain("reason");
  });
});

describe("isApprovalOutcome", () => {
  it("accepts the callback payload", () => {
    expect(isApprovalOutcome({ type: "tfsp-approval", ok: true, message: "grant approved" })).toBe(true);
  });
  it("rejects foreign messages", () => {
    expect(isApprovalOutcome({ type: "other" })).toBe(false);
    expect(isApprovalOutcome(null)).toBe(false);
    expect(isApprovalOutcome({ type: "tfsp-approval", ok: "yes", message: 1 })).toBe(false);
  });
});
