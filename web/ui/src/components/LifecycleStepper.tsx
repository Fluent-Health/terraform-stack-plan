import { For, Show } from "solid-js";
import type { LifecyclePhase } from "../api/client";
import { nowCaption, stepperSegments } from "../stepper";

/**
 * LifecycleStepper: one thin left-to-right row of phase segments (colour =
 * state only, no text inside). A ┆ divider separates plan-side from
 * gate/apply/verify-side. The active segment partially fills when real
 * per-stack progress is known. A single "happening now" caption (label +
 * plain-language description + sub-phase + progress) sits below. Rich
 * per-segment facts show on hover via the daisyUI CSS tooltip.
 */
export function LifecycleStepper(props: { phases: LifecyclePhase[] }) {
  const segs = () => stepperSegments(props.phases);
  return (
    <div>
      <div class="flex items-center gap-0.5">
        <For each={segs()}>
          {(s) => (
            <>
              <Show when={s.divider}>
                <span class="px-1 opacity-40 select-none">┆</span>
              </Show>
              <div class="tooltip tooltip-bottom flex-1 min-w-[10px]" data-tip={s.tip}>
                <Show when={s.fillPct !== undefined} fallback={<div class={`h-2 w-full rounded ${s.cls}`} />}>
                  <div class="h-2 w-full rounded bg-base-300 overflow-hidden">
                    <div
                      class="h-full rounded bg-info animate-pulse transition-[width] duration-500"
                      style={{ width: `${Math.max(4, s.fillPct!)}%` }}
                    />
                  </div>
                </Show>
              </div>
            </>
          )}
        </For>
      </div>
      <Show when={nowCaption(props.phases)}>{(cap) => <div class="text-xs opacity-70 mt-2">{cap()}</div>}</Show>
    </div>
  );
}
