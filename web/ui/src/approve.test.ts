import { describe, expect, it } from "vitest";
import { approveURL, defaultReason, isApprovalOutcome } from "./approve";

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

describe("defaultReason", () => {
  it("prefills an honest, decision-specific PAM reason", () => {
    const a = { pr: 813, class: "iam", target: "fh-prod-svc" };
    expect(defaultReason("approve", a)).toBe("Approving iam changes on fh-prod-svc for PR #813 (via tfstackplan)");
    expect(defaultReason("deny", a)).toBe("Denying iam changes on fh-prod-svc for PR #813 (via tfstackplan)");
  });
});
