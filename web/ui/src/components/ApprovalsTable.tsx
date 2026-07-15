import { A } from "@solidjs/router";
import { For, Show, createSignal } from "solid-js";
import type { PendingApproval } from "../api/client";
import { approveURL, defaultReason, runApproval } from "../approve";

/**
 * ApprovalsTable lists gate targets awaiting human action, with in-UI
 * Approve/Deny: the button opens the backend's incremental-consent popup and
 * the decision is made against PAM under the approving user's own identity —
 * a user without PAM approver IAM gets PAM's 403 verbatim. The PAM console
 * link remains as the fallback path.
 */
export function ApprovalsTable(props: { tier: string; approvals: PendingApproval[]; onDecided?: () => void }) {
  const [pending, setPending] = createSignal<{ approval: PendingApproval; decision: "approve" | "deny" } | null>(null);
  const [busy, setBusy] = createSignal(false);
  const [outcome, setOutcome] = createSignal<{ ok: boolean; message: string } | null>(null);
  const [reason, setReason] = createSignal("");
  const open = (approval: PendingApproval, decision: "approve" | "deny") => {
    setReason(defaultReason(decision, approval));
    setPending({ approval, decision });
  };

  const run = async () => {
    const p = pending();
    if (!p || !reason().trim()) return;
    setBusy(true);
    try {
      const res = await runApproval(approveURL(props.tier, p.approval.grant_name, p.decision, reason().trim()));
      setOutcome(res);
      if (res.ok) props.onDecided?.();
    } catch (e) {
      setOutcome({ ok: false, message: String(e) });
    } finally {
      setBusy(false);
      setPending(null);
    }
  };

  return (
    <div>
      <Show when={outcome()}>
        {(o) => (
          <div class={`alert ${o().ok ? "alert-success" : "alert-error"} mb-2 text-sm`}>
            <span>{o().message}</span>
            <button class="btn btn-ghost btn-xs" onClick={() => setOutcome(null)}>
              ✕
            </button>
          </div>
        )}
      </Show>
      <Show when={props.approvals.length > 0} fallback={<p class="opacity-60 text-sm">Nothing awaiting approval.</p>}>
        <table class="table table-sm">
          <thead>
            <tr>
              <th>PR</th>
              <th>environment</th>
              <th>class</th>
              <th>target</th>
              <th>state</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            <For each={props.approvals}>
              {(a) => (
                <tr class="hover">
                  <td>
                    <A class="link link-hover" href={`/pr/${a.pr}`}>
                      #{a.pr}
                    </A>
                  </td>
                  <td>{a.environment}</td>
                  <td>{a.class}</td>
                  <td class="font-mono text-xs">{a.target}</td>
                  <td>
                    <span class={`badge badge-sm ${a.state === "DENIED" || a.state === "REVOKED" ? "badge-error" : "badge-warning"}`}>
                      {a.state}
                    </span>
                  </td>
                  <td class="whitespace-nowrap">
                    <Show
                      when={a.grant_name !== ""}
                      fallback={
                        <a
                          class="link link-hover text-xs"
                          target="_blank"
                          rel="noreferrer"
                          href={`https://console.cloud.google.com/iam-admin/pam/grants/approvals?project=${a.target}`}
                        >
                          approve in PAM ↗
                        </a>
                      }
                    >
                      <button
                        class="btn btn-success btn-xs mr-1"
                        disabled={busy()}
                        onClick={() => open(a, "approve")}
                      >
                        Approve
                      </button>
                      <button
                        class="btn btn-error btn-outline btn-xs"
                        disabled={busy()}
                        onClick={() => open(a, "deny")}
                      >
                        Deny
                      </button>
                    </Show>
                  </td>
                </tr>
              )}
            </For>
          </tbody>
        </table>
      </Show>

      <Show when={pending()}>
        {(p) => (
          <div class="modal modal-open" role="dialog">
            <div class="modal-box">
              <h3 class="font-bold">
                {p().decision === "approve" ? "Approve" : "Deny"} {p().approval.class} on{" "}
                <span class="font-mono text-sm">{p().approval.target}</span>?
              </h3>
              <p class="py-1 text-sm opacity-70">
                PR #{p().approval.pr} · {p().approval.environment}. A Google consent popup will open; the decision is
                recorded in the PAM audit log under your identity.
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
                  class={`btn ${p().decision === "approve" ? "btn-success" : "btn-error"}`}
                  disabled={busy() || !reason().trim()}
                  onClick={run}
                >
                  {p().decision === "approve" ? "Approve" : "Deny"}
                </button>
              </div>
            </div>
            <div class="modal-backdrop" onClick={() => setPending(null)} />
          </div>
        )}
      </Show>
    </div>
  );
}
