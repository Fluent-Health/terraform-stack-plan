import { For, Index, Show, Suspense, createEffect, createMemo, createResource, createSignal, onCleanup } from "solid-js";
import { useSearchParams } from "@solidjs/router";
import { api, executionEventsURL, type ExecutionSummary, type StackState } from "../api/client";
import { approvalsByTarget, contextKind, groupByProject } from "../prdata";
import { GateApproval } from "./GateApproval";
import { LifecycleStepper } from "./LifecycleStepper";
import { StackDetail } from "./StackDetail";
import { SEM_DOT, statusSem } from "../status";

/** TierPanel: one tier's current run for a PR — progress blocks + context chips + project groups + drill-in. */
export function TierPanel(props: {
  tier: string;
  summary: ExecutionSummary;
  contexts: ExecutionSummary[];
  onSuperseded?: () => void;
}) {
  const [searchParams, setSearchParams] = useSearchParams();
  const isExpanded = () => searchParams.tier === props.tier;

  const [detail, { refetch: refetchDetail }] = createResource(
    () => ({ tier: props.tier, id: props.summary.id }),
    (k) => api.execution(k.tier, k.id),
  );
  const [lifecycle, { refetch: refetchLifecycle }] = createResource(
    () => ({ tier: props.tier, pr: props.summary.pr }),
    (k) => api.lifecycle(k.tier, k.pr),
  );
  createEffect(() => {
    const es = new EventSource(executionEventsURL(props.tier, props.summary.id));
    let t: ReturnType<typeof setTimeout> | undefined;
    es.onmessage = () => {
      clearTimeout(t);
      t = setTimeout(() => {
        refetchDetail();
        refetchLifecycle();
      }, 300);
    };
    es.addEventListener("superseded", () => props.onSuperseded?.());
    onCleanup(() => {
      clearTimeout(t);
      es.close();
    });
  });
  const [open, setOpen] = createSignal<string | undefined>();

  // Pending approvals gating this PR's projects on this tier, for the
  // in-context Approve/Deny affordance on gated project-group headers.
  const [approvals, { refetch: refetchApprovals }] = createResource(
    () => props.tier,
    (tier) => api.approvals(tier),
  );
  const gates = createMemo(() => approvalsByTarget(approvals() ?? [], props.summary.pr));
  const onDecided = () => {
    refetchApprovals();
    refetchDetail();
  };

  return (
    <section class="card bg-base-200 border border-base-300">
      <div class="card-body p-4 gap-3">
        <div class="flex items-center gap-2 border-b border-base-300 pb-2">
          <span
            class="inline-block w-2 h-2 rounded-full"
            style={{ background: SEM_DOT[statusSem(props.summary.status)] }}
          />
          <span class="font-bold text-sm uppercase tracking-wide">{props.tier}</span>

          <button
            class="ml-auto btn btn-xs btn-ghost gap-1 px-2 text-primary"
            onClick={() => setSearchParams({ tier: isExpanded() ? undefined : props.tier })}
            title={isExpanded() ? "Show all tiers side-by-side" : `Focus on ${props.tier} tier`}
          >
            <Show when={isExpanded()} fallback={
              <>🔎 <span class="hidden sm:inline">Focus</span></>
            }>
              <>🔀 <span class="hidden sm:inline">Show All</span></>
            </Show>
          </button>
        </div>
        <div class="flex flex-wrap gap-2">
          <For each={props.contexts}>
            {(c) => (
              <span class="inline-flex items-center gap-1 text-xs badge badge-ghost">
                <span class="w-1.5 h-1.5 rounded-full" style={{ background: SEM_DOT[statusSem(c.status)] }} />
                {contextKind(c.context)}
              </span>
            )}
          </For>
        </div>
        <Suspense fallback={<span class="loading loading-dots loading-sm" />}>
          <Show when={detail()}>
            {(d) => (
              <>
                <Show when={lifecycle()} fallback={<span class="loading loading-dots loading-sm" />}>
                  {(lp) => <LifecycleStepper phases={lp()} />}
                </Show>
                <Index each={groupByProject(d().graph?.stacks ?? [])}>
                  {(g) => (
                    <div class="rounded-field border border-base-300" classList={{ "border-error/50": g().failed }}>
                      <div class="px-3 py-2 bg-base-100 rounded-t-field flex items-center gap-2">
                        <span class="text-xs font-mono">{g().project}</span>
                        <Show when={gates().get(g().project)}>
                          {(a) => <GateApproval tier={props.tier} approval={a()} onDecided={onDecided} />}
                        </Show>
                      </div>
                      <Index each={g().stacks}>
                        {(s) => (
                          <div class="border-t border-base-300">
                            <button
                              class="w-full flex items-center gap-2 px-3 py-2 text-sm hover:bg-base-100"
                              onClick={() => setOpen(open() === s().path ? undefined : s().path)}
                            >
                              <span class="font-mono text-xs">{s().path}</span>
                              <span class="text-xs opacity-50">{countsLabel(s())}</span>
                              <span
                                class="ml-auto inline-flex items-center gap-1 text-xs"
                                style={{ color: SEM_DOT[statusSem(s().status ?? "")] }}
                              >
                                ● {s().status || "pending"}
                              </span>
                            </button>
                            <Show when={open() === s().path}>
                              <div class="px-3 pb-3">
                                <StackDetail tier={props.tier} exec={d().ID} stack={s()} />
                              </div>
                            </Show>
                          </div>
                        )}
                      </Index>
                    </div>
                  )}
                </Index>
              </>
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
