// The SPA side of the PAM approve/deny popup flow. Pure helpers are separated
// from the window plumbing so they unit-test without a browser.

export interface ApprovalOutcome {
  type: "tfsp-approval";
  ok: boolean;
  message: string;
}

/** approveURL builds the backend consent-popup entry point. */
export function approveURL(tier: string, grant: string, decision: "approve" | "deny", reason: string): string {
  const q = new URLSearchParams({ tier, grant, decision });
  if (reason) q.set("reason", reason);
  return `/auth/approve?${q}`;
}

/**
 * defaultReason prefills the decision modal — PAM requires a non-empty reason
 * (it is NOT optional), so the field starts with an honest, editable default.
 */
export function defaultReason(
  decision: "approve" | "deny",
  a: { pr: number; class: string; target: string },
): string {
  const verb = decision === "approve" ? "Approving" : "Denying";
  return `${verb} ${a.class} changes on ${a.target} for PR #${a.pr} (via tfstackplan)`;
}

/** isApprovalOutcome guards a postMessage payload from the popup. */
export function isApprovalOutcome(data: unknown): data is ApprovalOutcome {
  return (
    typeof data === "object" &&
    data !== null &&
    (data as Record<string, unknown>).type === "tfsp-approval" &&
    typeof (data as Record<string, unknown>).ok === "boolean" &&
    typeof (data as Record<string, unknown>).message === "string"
  );
}

/**
 * runApproval opens the consent popup and resolves with the outcome posted
 * back by the callback page. Rejects when the popup is blocked; resolves a
 * failure outcome when the user just closes the window.
 */
export function runApproval(url: string): Promise<ApprovalOutcome> {
  return new Promise((resolve, reject) => {
    const popup = window.open(url, "tfsp-approve", "popup,width=520,height=680");
    if (!popup) {
      reject(new Error("popup blocked — allow popups for this site"));
      return;
    }
    const onMessage = (e: MessageEvent) => {
      if (e.origin !== window.location.origin || !isApprovalOutcome(e.data)) return;
      cleanup();
      resolve(e.data);
    };
    const closedPoll = setInterval(() => {
      if (popup.closed) {
        cleanup();
        resolve({ type: "tfsp-approval", ok: false, message: "window closed before completing" });
      }
    }, 500);
    const cleanup = () => {
      window.removeEventListener("message", onMessage);
      clearInterval(closedPoll);
    };
    window.addEventListener("message", onMessage);
  });
}
