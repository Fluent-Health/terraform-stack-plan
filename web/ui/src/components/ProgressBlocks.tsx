import { For, Show } from "solid-js";
import type { StackState } from "../api/client";
import { statusSem } from "../status";

const BLK: Record<string, string> = {
  ok: "bg-success", running: "bg-info animate-pulse", failed: "bg-error",
  waiting: "bg-warning", idle: "bg-base-300",
};

/** ProgressBlocks: one block per changed stack, colored by status — real work
 * throughput, parallel-aware, failures visible. Replaces the fixed per-stage
 * tick-segment progress indicator. */
export function ProgressBlocks(props: { stacks: StackState[]; caption?: string }) {
  return (
    <div>
      <div class="flex flex-wrap gap-1">
        <For each={props.stacks}>
          {(s) => (
            <div class="tooltip tooltip-bottom flex-1 min-w-[26px]" data-tip={`${s.path} — ${s.status || "pending"}`}>
              <div
                class={`h-6 w-full rounded ${BLK[statusSem(s.status ?? "")]}`}
              />
            </div>
          )}
        </For>
      </div>
      <Show when={props.caption}>
        <div class="text-xs opacity-70 mt-2">{props.caption}</div>
      </Show>
    </div>
  );
}
