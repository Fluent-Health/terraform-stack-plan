import { Show, createSignal } from "solid-js";
import type { StackState } from "../api/client";
import { LogView } from "./LogView";
import { PlanFragment } from "./PlanFragment";
import { statusSem } from "../status";

/** StackDetail: inline Plan/Log for one stack. Failed stacks default to Log. */
export function StackDetail(props: { tier: string; exec: string; stack: StackState }) {
  const failed = () => statusSem(props.stack.status ?? "") === "failed";
  const [tab, setTab] = createSignal<"log" | "plan">(failed() ? "log" : "plan");
  return (
    <div class="mt-2 rounded-field bg-base-100 border border-base-300 p-3">
      <Show when={failed() && props.stack.detail}>
        <div class="alert alert-error text-xs font-mono mb-2 whitespace-pre-wrap">{props.stack.detail}</div>
      </Show>
      <div role="tablist" class="tabs tabs-border mb-2">
        <a role="tab" class="tab" classList={{ "tab-active": tab() === "plan" }} onClick={() => setTab("plan")}>
          Plan
        </a>
        <a role="tab" class="tab" classList={{ "tab-active": tab() === "log" }} onClick={() => setTab("log")}>
          Log
        </a>
      </div>
      <Show
        when={tab() === "log"}
        fallback={<PlanFragment tier={props.tier} exec={props.exec} stack={props.stack.path} />}
      >
        <LogView tier={props.tier} exec={props.exec} stack={props.stack.path} />
      </Show>
    </div>
  );
}
