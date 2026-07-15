import { Show, createSignal } from "solid-js";
import type { PendingApproval } from "../api/client";
import { approveURL, defaultReason, runApproval } from "../approve";

/**
 * GateApproval: in-context approve/deny for one gated project group on the PR
 * page. Same flow as ApprovalsTable (reason capture → PAM consent popup →
 * outcome), just anchored to a project-group header instead of a table row.
 */
export function GateApproval(props: { tier: string; approval: PendingApproval; onDecided: () => void }) {
  const [pending, setPending] = createSignal<"approve" | "deny" | null>(null);
  const [busy, setBusy] = createSignal(false);
  const [outcome, setOutcome] = createSignal<{ ok: boolean; message: string } | null>(null);
  const [reason, setReason] = createSignal("");
  const open = (decision: "approve" | "deny") => {
    setReason(defaultReason(decision, props.approval));
    setPending(decision);
  };

  const run = async () => {
    const decision = pending();
    if (!decision || !reason().trim()) return;
    setBusy(true);
    try {
      const res = await runApproval(approveURL(props.tier, props.approval.grant_name, decision, reason().trim()));
      setOutcome(res);
      if (res.ok) props.onDecided();
    } catch (e) {
      setOutcome({ ok: false, message: String(e) });
    } finally {
      setBusy(false);
      setPending(null);
    }
  };

  return (
    <span class="ml-auto flex items-center gap-2 normal-case font-sans">
      <span class="badge badge-warning badge-sm">{props.approval.class}</span>
      <Show when={outcome()}>
        {(o) => (
          <span class={`text-xs flex items-center gap-1 ${o().ok ? "text-success" : "text-error"}`}>
            {o().message}
            <button class="btn btn-ghost btn-xs" onClick={() => setOutcome(null)}>
              ✕
            </button>
          </span>
        )}
      </Show>
      <Show
        when={props.approval.grant_name !== ""}
        fallback={
          <a
            class="link link-hover text-xs"
            target="_blank"
            rel="noreferrer"
            href={`https://console.cloud.google.com/iam-admin/pam/grants/approvals?project=${props.approval.target}`}
          >
            approve in PAM ↗
          </a>
        }
      >
        <button class="btn btn-success btn-xs" disabled={busy()} onClick={() => open("approve")}>
          Approve
        </button>
        <button class="btn btn-error btn-outline btn-xs" disabled={busy()} onClick={() => open("deny")}>
          Deny
        </button>
      </Show>

      <Show when={pending()}>
        {(decision) => (
          <div class="modal modal-open" role="dialog">
            <div class="modal-box">
              <h3 class="font-bold">
                {decision() === "approve" ? "Approve" : "Deny"} {props.approval.class} on{" "}
                <span class="font-mono text-sm">{props.approval.target}</span>?
              </h3>
              <p class="py-1 text-sm opacity-70">
                PR #{props.approval.pr} · {props.approval.environment}. A Google consent popup will open; the decision
                is recorded in the PAM audit log under your identity.
              </p>
              <label class="text-xs opacity-70">reason (required — recorded in the PAM audit log)</label>
              <textarea
                class="textarea w-full"
                placeholder="reason (required)"
                maxlength="512"
                value={reason()}
                onInput={(e) => setReason(e.currentTarget.value)}
              />
              <div class="modal-action">
                <button class="btn btn-ghost" onClick={() => setPending(null)}>
                  Cancel
                </button>
                <button
                  class={`btn ${decision() === "approve" ? "btn-success" : "btn-error"}`}
                  disabled={busy() || !reason().trim()}
                  onClick={run}
                >
                  {decision() === "approve" ? "Approve" : "Deny"}
                </button>
              </div>
            </div>
            <div class="modal-backdrop" onClick={() => setPending(null)} />
          </div>
        )}
      </Show>
    </span>
  );
}
