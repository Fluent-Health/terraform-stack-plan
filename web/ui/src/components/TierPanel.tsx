import { For, Show, Suspense, createEffect, createResource, createSignal, onCleanup } from "solid-js";
import { api, executionEventsURL, type ExecutionSummary, type StackState } from "../api/client";
import { groupByProject } from "../prdata";
import { Stepper } from "./Stepper";
import { StackDetail } from "./StackDetail";
import { SEM_DOT, statusSem } from "../status";

/** TierPanel: one tier's current run for a PR — stepper + project groups + drill-in. */
export function TierPanel(props: { tier: string; summary: ExecutionSummary }) {
  const [detail, { refetch }] = createResource(
    () => ({ tier: props.tier, id: props.summary.id }),
    (k) => api.execution(k.tier, k.id),
  );
  createEffect(() => {
    const es = new EventSource(executionEventsURL(props.tier, props.summary.id));
    let t: ReturnType<typeof setTimeout> | undefined;
    es.onmessage = () => {
      clearTimeout(t);
      t = setTimeout(() => refetch(), 300);
    };
    onCleanup(() => {
      clearTimeout(t);
      es.close();
    });
  });
  const [open, setOpen] = createSignal<string | undefined>();

  return (
    <section class="card bg-base-200 border border-base-300">
      <div class="card-body p-4 gap-3">
        <div class="flex items-center gap-2">
          <span
            class="inline-block w-2 h-2 rounded-full"
            style={{ background: SEM_DOT[statusSem(props.summary.status)] }}
          />
          <span class="font-bold text-sm">{props.tier}</span>
          <span class="ml-auto badge badge-ghost badge-sm font-mono">{props.summary.context}</span>
        </div>
        <Stepper
          phase={props.summary.phase}
          status={props.summary.status}
          caption={`${props.summary.phase || "queued"} · ${new Date(props.summary.created_at).toLocaleTimeString()}`}
        />
        <Suspense fallback={<span class="loading loading-dots loading-sm" />}>
          <Show when={detail()}>
            {(d) => (
              <For each={groupByProject(d().graph?.stacks ?? [])}>
                {(g) => (
                  <div class="rounded-field border border-base-300" classList={{ "border-error/50": g.failed }}>
                    <div class="px-3 py-2 bg-base-100 text-xs font-mono rounded-t-field">{g.project}</div>
                    <For each={g.stacks}>
                      {(s) => (
                        <div class="border-t border-base-300">
                          <button
                            class="w-full flex items-center gap-2 px-3 py-2 text-sm hover:bg-base-100"
                            onClick={() => setOpen(open() === s.path ? undefined : s.path)}
                          >
                            <span class="font-mono text-xs">{s.path}</span>
                            <span class="text-xs opacity-50">{countsLabel(s)}</span>
                            <span
                              class="ml-auto inline-flex items-center gap-1 text-xs"
                              style={{ color: SEM_DOT[statusSem(s.status ?? "")] }}
                            >
                              ● {s.status || "pending"}
                            </span>
                          </button>
                          <Show when={open() === s.path}>
                            <div class="px-3 pb-3">
                              <StackDetail tier={props.tier} exec={d().ID} stack={s} />
                            </div>
                          </Show>
                        </div>
                      )}
                    </For>
                  </div>
                )}
              </For>
            )}
          </Show>
        </Suspense>
      </div>
    </section>
  );
}

function countsLabel(s: StackState): string {
  const c = s.counts;
  if (!c) return "";
  const p: string[] = [];
  if (c.add) p.push(`+${c.add}`);
  if (c.change) p.push(`~${c.change}`);
  if (c.replace) p.push(`±${c.replace}`);
  if (c.destroy) p.push(`−${c.destroy}`);
  if (c.move) p.push(`↔${c.move}`);
  return p.join(" ");
}
