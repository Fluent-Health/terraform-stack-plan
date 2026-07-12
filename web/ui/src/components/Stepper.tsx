import { For } from "solid-js";
import { STAGES, stageStates, type StageState } from "../stepper";

const SEG: Record<StageState, string> = {
  done: "bg-success",
  now: "bg-info animate-pulse",
  pending: "bg-base-300",
  failed: "bg-error",
};

/** Stepper: state-colored tick segments (no inline text) + a happening-now caption. */
export function Stepper(props: { phase: string; status: string; caption?: string }) {
  const states = () => stageStates(props.phase, props.status);
  return (
    <div>
      <div class="flex gap-1">
        <For each={STAGES}>
          {(name, i) => (
            <div
              class={`h-2 flex-1 rounded ${SEG[states()[i()]]}`}
              title={`${name} — ${states()[i()]}`}
            />
          )}
        </For>
      </div>
      <div class="text-xs opacity-70 mt-2">{props.caption ?? props.phase}</div>
    </div>
  );
}
