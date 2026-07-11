import { A } from "@solidjs/router";
import { For, Show } from "solid-js";
import type { PendingApproval } from "../api/client";

/**
 * ApprovalsTable lists gate targets awaiting human action. Read-only for now:
 * in-UI Approve/Deny (incremental consent) is the next increment; until then
 * the PAM console link is the action, exactly like the tier viewer had.
 */
export function ApprovalsTable(props: { approvals: PendingApproval[] }) {
  return (
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
                <td>
                  <a
                    class="link link-hover text-xs"
                    target="_blank"
                    rel="noreferrer"
                    href={`https://console.cloud.google.com/iam-admin/pam/grants/approvals?project=${a.target}`}
                  >
                    approve in PAM ↗
                  </a>
                </td>
              </tr>
            )}
          </For>
        </tbody>
      </table>
    </Show>
  );
}
