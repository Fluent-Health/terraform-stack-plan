import { A } from "@solidjs/router";
import { For, Show } from "solid-js";
import type { ExecutionSummary } from "../api/client";

const STATUS_BADGE: Record<string, string> = {
  success: "badge-success",
  failure: "badge-error",
  in_progress: "badge-info",
};

export function StatusBadge(props: { status: string; superseded?: boolean }) {
  return (
    <span class={`badge badge-sm ${props.superseded ? "badge-ghost" : STATUS_BADGE[props.status] ?? "badge-ghost"}`}>
      {props.superseded ? "superseded" : props.status || "queued"}
    </span>
  );
}

/** ExecTable lists execution summaries for one tier, linking each to its view. */
export function ExecTable(props: { tier: string; executions: ExecutionSummary[] }) {
  return (
    <table class="table table-sm">
      <thead>
        <tr>
          <th>when</th>
          <th>PR</th>
          <th>context</th>
          <th>phase</th>
          <th>status</th>
        </tr>
      </thead>
      <tbody>
        <For each={props.executions}>
          {(e) => (
            <tr class="hover">
              <td>
                <A class="link link-hover whitespace-nowrap" href={`/t/${props.tier}/e/${e.id}`}>
                  {new Date(e.created_at).toLocaleString()}
                </A>
              </td>
              <td>
                <Show when={e.pr > 0} fallback={<span class="opacity-40">—</span>}>
                  <A class="link link-hover" href={`/pr/${e.pr}`}>
                    #{e.pr}
                  </A>
                </Show>
              </td>
              <td class="font-mono text-xs">{e.context}</td>
              <td>{e.phase}</td>
              <td>
                <StatusBadge status={e.status} superseded={e.superseded_by !== ""} />
              </td>
            </tr>
          )}
        </For>
      </tbody>
    </table>
  );
}
