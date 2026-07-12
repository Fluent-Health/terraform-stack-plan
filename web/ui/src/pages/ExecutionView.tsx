import { useNavigate, useParams } from "@solidjs/router";
import { For, Show, Suspense, createEffect, createMemo, createResource, createSignal, onCleanup } from "solid-js";
import type { ExecutionDetail, StackState } from "../api/client";
import { api, executionEventsURL } from "../api/client";
import { StatusBadge } from "../components/ExecTable";
import { LogView } from "../components/LogView";
import { PlanFragment } from "../components/PlanFragment";

/**
 * ExecutionView: the live briefing for one execution. The SSE change stream
 * carries no payload by design — on every `changed` the execution JSON is
 * refetched and Solid's fine-grained reactivity patches only what moved.
 * `superseded` navigates to the successor. No page reloads anywhere.
 */
export function ExecutionView() {
  const params = useParams();
  const navigate = useNavigate();
  const [detail, { refetch }] = createResource(
    () => ({ tier: params.tier!, id: params.id! }),
    (k) => api.execution(k.tier, k.id),
  );

  createEffect(() => {
    const tier = params.tier!;
    const id = params.id!;
    const es = new EventSource(executionEventsURL(tier, id));
    let timer: ReturnType<typeof setTimeout> | undefined;
    es.onmessage = () => {
      clearTimeout(timer);
      timer = setTimeout(() => refetch(), 300);
    };
    es.addEventListener("superseded", (e) => {
      navigate(`/t/${tier}/e/${(e as MessageEvent).data}`);
    });
    onCleanup(() => {
      clearTimeout(timer);
      es.close();
    });
  });

  return (
    <Suspense fallback={<span class="loading loading-dots" />}>
      <Show when={detail()}>{(d) => <Briefing tier={params.tier!} detail={d()} />}</Show>
    </Suspense>
  );
}

const DONE = new Set(["planned", "safe", "nochange", "failed", "aborted", "gated", "moving"]);

function Briefing(props: { tier: string; detail: ExecutionDetail }) {
  // Tolerate nulls from older tiers: pre-v0.25.2 serves marshalled a
  // zero-stack graph as "stacks": null despite the contract's array.
  const stacks = createMemo(() => props.detail.graph?.stacks ?? []);
  const progress = createMemo(() => {
    const done = stacks().filter((s) => DONE.has(s.status ?? "")).length;
    return {
      done,
      total: stacks().length,
      pct: props.detail.ProgressPct ?? 0,
      label: props.detail.ProgressLabel ?? props.detail.Phase ?? ""
    };
  });
  const [selected, setSelected] = createSignal<StackState | undefined>(undefined);

  return (
    <div class="space-y-4">
      <header class="card bg-base-100 shadow-sm">
        <div class="card-body p-4 gap-2">
          <div class="flex flex-wrap items-center gap-3">
            <h1 class="text-lg font-bold">
              {props.detail.Repo} · #{props.detail.PR} ·{" "}
              <span class="font-mono text-sm">{props.detail.SHA.slice(0, 12)}</span>
            </h1>
            <span class="badge badge-outline">{props.detail.StatusContext || props.detail.Environment}</span>
            <Show when={progress().label}>
              <span class="badge badge-info badge-outline uppercase">{progress().label}</span>
            </Show>
            <StatusBadge status={props.detail.Status} superseded={props.detail.SupersededBy !== ""} />
          </div>
          <div class="flex items-center gap-3">
            <progress class="progress progress-primary flex-1" value={progress().pct} max="100" />
            <span class="text-sm opacity-70 whitespace-nowrap">
              {progress().done}/{progress().total} done
            </span>
          </div>
        </div>
      </header>

      <Show when={(props.detail.gates ?? []).length > 0}>
        <section class="card bg-base-100 shadow-sm border-l-4 border-warning">
          <div class="card-body p-4">
            <h2 class="card-title text-base">approval gates</h2>
            <table class="table table-sm">
              <tbody>
                <For each={props.detail.gates}>
                  {(g) => (
                    <tr>
                      <td>{g.Class}</td>
                      <td class="font-mono text-xs">{g.Target}</td>
                      <td>
                        <span class={`badge badge-sm ${g.State === "ACTIVE" ? "badge-success" : "badge-warning"}`}>
                          {g.State}
                        </span>
                      </td>
                    </tr>
                  )}
                </For>
              </tbody>
            </table>
          </div>
        </section>
      </Show>

      <section class="card bg-base-100 shadow-sm">
        <div class="card-body p-4">
          <h2 class="card-title text-base">stacks</h2>
          <table class="table table-sm">
            <tbody>
              <For each={stacks()}>
                {(s) => (
                  <tr
                    class="hover cursor-pointer"
                    classList={{ "bg-base-200": selected()?.path === s.path }}
                    onClick={() => setSelected(selected()?.path === s.path ? undefined : s)}
                  >
                    <td class="font-mono text-xs">{s.path}</td>
                    <td class="text-xs opacity-70">{countsLabel(s)}</td>
                    <td>
                      <span class={`badge badge-sm ${stackBadge(s.status ?? "")}`}>{s.status || "pending"}</span>
                    </td>
                  </tr>
                )}
              </For>
            </tbody>
          </table>
        </div>
      </section>

      <Show when={selected()} keyed>
        {(s) => <StackDetail tier={props.tier} exec={props.detail.ID} stack={s} />}
      </Show>
    </div>
  );
}

function StackDetail(props: { tier: string; exec: string; stack: StackState }) {
  const [tab, setTab] = createSignal<"log" | "plan">(props.stack.status === "failed" ? "log" : "plan");
  return (
    <section class="card bg-base-100 shadow-sm">
      <div class="card-body p-4">
        <h2 class="card-title text-base font-mono">{props.stack.path}</h2>
        <div role="tablist" class="tabs tabs-border">
          <a role="tab" class="tab" classList={{ "tab-active": tab() === "plan" }} onClick={() => setTab("plan")}>
            Plan
          </a>
          <a role="tab" class="tab" classList={{ "tab-active": tab() === "log" }} onClick={() => setTab("log")}>
            Log
          </a>
        </div>
        <Show when={tab() === "log"} fallback={<PlanFragment tier={props.tier} exec={props.exec} stack={props.stack.path} />}>
          <LogView tier={props.tier} exec={props.exec} stack={props.stack.path} />
        </Show>
      </div>
    </section>
  );
}

function stackBadge(status: string): string {
  switch (status) {
    case "failed":
    case "aborted":
      return "badge-error";
    case "gated":
      return "badge-warning";
    case "safe":
    case "nochange":
    case "planned":
      return "badge-success";
    case "running":
    case "initializing":
    case "initialized":
      return "badge-info";
    default:
      return "badge-ghost";
  }
}

function countsLabel(s: StackState): string {
  const c = s.counts;
  if (!c) return "";
  const parts: string[] = [];
  if (c.add) parts.push(`+${c.add}`);
  if (c.change) parts.push(`~${c.change}`);
  if (c.replace) parts.push(`±${c.replace}`);
  if (c.destroy) parts.push(`−${c.destroy}`);
  if (c.move) parts.push(`↔${c.move}`);
  return parts.join(" ");
}
