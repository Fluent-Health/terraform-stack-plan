import { For, Index, Show, Suspense, createEffect, createMemo, createResource, createSignal, onCleanup } from "solid-js";
import { useSearchParams } from "@solidjs/router";
import { api, executionEventsURL, type ExecutionSummary, type StackState } from "../api/client";
import { approvalsByTarget, changedStackCount, groupByComponent, rollupChangeCounts, stackHasChanges } from "../prdata";
import { GateApproval } from "./GateApproval";
import { LifecycleStepper } from "./LifecycleStepper";
import { StackDetail } from "./StackDetail";
import { DepGraph } from "./DepGraph";
import { SEM_DOT, statusSem } from "../status";
import { graphCounts } from "../dag";

/**
 * TierPanel: one tier's current run for a PR — lifecycle stepper + gates strip
 * + component groups + drill-in.
 *
 * Refresh without flicker: every resource is read through `.latest`, so the
 * SSE-driven refetches swap data in place instead of re-triggering Suspense
 * (which unmounted the whole panel on every tick). Only the FIRST load shows
 * the skeleton; the stack drill-in has its own Suspense boundary so opening a
 * stack never collapses the panel around it.
 */
export function TierPanel(props: {
  tier: string;
  summary: ExecutionSummary;
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
  const [dagOpen, setDagOpen] = createSignal(false);

  // Pending approvals gating this PR's targets on this tier, rendered in a
  // standalone gates strip decoupled from component-group headers so every
  // pending approval for this PR/tier is visible regardless of grouping.
  const [approvals, { refetch: refetchApprovals }] = createResource(
    () => props.tier,
    (tier) => api.approvals(tier),
  );
  const gates = createMemo(() => approvalsByTarget(approvals.latest ?? [], props.summary.pr));
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
        <Show when={detail.latest} fallback={<PanelSkeleton />}>
          {(d) => (
            <>
              <Show when={lifecycle.latest} fallback={<div class="skeleton h-2 w-full rounded" />}>
                {(lp) => <LifecycleStepper phases={lp()} />}
              </Show>
              <ChangeSummary stacks={d().graph?.stacks ?? []} />
              <Show when={[...gates().values()].length > 0}>
                <div class="rounded-field border border-warning/40 bg-warning/5 p-2 flex flex-col gap-2">
                  <span class="text-xs font-semibold text-warning">⚠ needs approval</span>
                  <For each={[...gates().values()]}>
                    {(a) => (
                      <div class="flex items-center gap-2 text-xs">
                        <span class="font-mono">{a.target}</span>
                        <GateApproval tier={props.tier} approval={a} onDecided={onDecided} />
                      </div>
                    )}
                  </For>
                </div>
              </Show>
              <Index each={groupByComponent(d().graph?.stacks ?? [])}>
                {(g) => {
                  const groupChanged = () => changedStackCount(g().stacks);
                  return (
                    <div class="rounded-field border border-base-300" classList={{ "border-error/50": g().failed }}>
                      <div
                        class="px-3 py-2 bg-base-100 rounded-t-field flex items-center gap-2"
                        classList={{ "opacity-60": groupChanged() === 0 && !g().failed }}
                      >
                        <span class="text-xs font-mono">{g().component}</span>
                        <Show
                          when={rollupChangeCounts(g().stacks)}
                          fallback={<span class="text-xs opacity-50">no changes</span>}
                        >
                          {(c) => <span class="text-xs font-mono font-semibold">{c()}</span>}
                        </Show>
                        <span class="text-xs opacity-50 ml-auto">{g().stacks.length}</span>
                      </div>
                      <Index each={g().stacks}>
                        {(s) => (
                          <div class="border-t border-base-300">
                            <button
                              class="w-full flex items-center gap-2 px-3 py-2 text-sm hover:bg-base-100"
                              classList={{ "opacity-60": !stackHasChanges(s()) && statusSem(s().status ?? "") === "ok" }}
                              onClick={() => setOpen(open() === s().path ? undefined : s().path)}
                            >
                              <span class="font-mono text-xs">{s().path}</span>
                              <Show
                                when={stackHasChanges(s())}
                                fallback={
                                  <Show when={statusSem(s().status ?? "") === "ok"}>
                                    <span class="text-xs opacity-50">— no changes</span>
                                  </Show>
                                }
                              >
                                <span class="font-mono text-xs font-semibold">{countsLabel(s())}</span>
                              </Show>
                              <span
                                class="ml-auto inline-flex items-center gap-1 text-xs"
                                style={{ color: SEM_DOT[statusSem(s().status ?? "")] }}
                              >
                                ● {s().status || "pending"}
                              </span>
                            </button>
                            <Show when={open() === s().path}>
                              <div class="stack-open">
                                <div class="px-3 pb-3">
                                  <Suspense fallback={<StackSkeleton />}>
                                    <StackDetail tier={props.tier} exec={d().ID} stack={s()} />
                                  </Suspense>
                                </div>
                              </div>
                            </Show>
                          </div>
                        )}
                      </Index>
                    </div>
                  );
                }}
              </Index>
              {(() => {
                const gc = graphCounts(d().graph);
                return (
                  <Show when={gc.stacks > 0}>
                    <div class="rounded-field border border-base-300">
                      <button
                        class="w-full flex items-center gap-2 px-3 py-2 text-xs hover:bg-base-100"
                        onClick={() => setDagOpen(!dagOpen())}
                      >
                        <span>{dagOpen() ? "▾" : "▸"}</span>
                        <span>
                          Dependency graph ({gc.stacks} stacks, {gc.edges} edges)
                        </span>
                      </button>
                      <Show when={dagOpen()}>
                        <div class="stack-open">
                          <div class="p-2">
                            <DepGraph detail={d()} />
                          </div>
                        </div>
                      </Show>
                    </div>
                  </Show>
                );
              })()}
            </>
          )}
        </Show>
      </div>
    </section>
  );
}

/** ChangeSummary: the scannable "what changed" tagline under the stepper. */
function ChangeSummary(props: { stacks: StackState[] }) {
  const rollup = () => rollupChangeCounts(props.stacks);
  const changed = () => changedStackCount(props.stacks);
  return (
    <Show
      when={rollup()}
      fallback={
        <Show when={props.stacks.length > 0}>
          <div class="text-sm opacity-60">No changes across {props.stacks.length} stacks</div>
        </Show>
      }
    >
      {(r) => (
        <div class="flex items-baseline gap-2">
          <span class="font-mono text-base font-semibold">{r()}</span>
          <span class="text-xs opacity-60">
            {changed()} of {props.stacks.length} stacks changed
          </span>
        </div>
      )}
    </Show>
  );
}

/** PanelSkeleton: first-load placeholder shaped like the real panel, so the
 * page height doesn't jump when data lands. */
function PanelSkeleton() {
  return (
    <div class="flex flex-col gap-3" aria-busy="true">
      <div class="skeleton h-2 w-full rounded" />
      <div class="skeleton h-5 w-40 rounded" />
      <div class="skeleton h-24 w-full rounded-field" />
      <div class="skeleton h-24 w-full rounded-field" />
    </div>
  );
}

/** StackSkeleton: drill-in placeholder while the plan fragment/log loads. */
function StackSkeleton() {
  return (
    <div class="mt-2 rounded-field bg-base-100 border border-base-300 p-3 flex flex-col gap-2" aria-busy="true">
      <div class="skeleton h-6 w-32 rounded" />
      <div class="skeleton h-4 w-full rounded" />
      <div class="skeleton h-4 w-5/6 rounded" />
      <div class="skeleton h-4 w-2/3 rounded" />
    </div>
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
