import { For, Show, Suspense, createEffect, createMemo, createResource, createSignal, onCleanup, onMount } from "solid-js";
import { A } from "@solidjs/router";
import { api, type ExecutionDetail, type ExecutionSummary, type PendingApproval, type PRView, type StackState } from "../api/client";
import { STAGE_LABEL, contextKind, distinctPRs, mergeBadge, prLifecycleStage, primaryExec, relativeTime, rollupChangeCounts, stageSem } from "../prdata";
import { SEM_DOT } from "../status";

type Tagged = ExecutionSummary & { tier: string };

/** Prs: the landing list of active PRs, newest-first, with per-tier lifecycle
 * stage, merge/automerge badge, author, last-updated, and a rolled-up change
 * summary. Identity/merge come from a per-PR fan-out over api.pr (tier-agnostic,
 * first reachable tier wins, same degrade pattern as PrIdentity); a light 10s
 * poll keeps the list current without touching the SSE model. */
export function Prs() {
  createEffect(() => {
    document.title = "PRs · tfstackplan";
  });

  const [tick, setTick] = createSignal(0);
  onMount(() => {
    const h = setInterval(() => setTick((t) => t + 1), 10_000);
    onCleanup(() => clearInterval(h));
  });

  const [tiers] = createResource(api.tiers);
  const [all] = createResource(
    () => (tiers() ? { ts: tiers()!, tick: tick() } : undefined),
    async (k) => {
      const per = await Promise.all(
        k.ts.map(async (t) => {
          const [execs, approvals] = await Promise.all([
            api.executions(t.name, { limit: 200 }).catch(() => [] as ExecutionSummary[]),
            api.approvals(t.name).catch(() => [] as PendingApproval[]),
          ]);
          return {
            rows: execs.map((e): Tagged => ({ ...e, tier: t.name })),
            approvals: approvals.map((a) => ({ ...a, tier: t.name })),
          };
        }),
      );
      return {
        rows: per.flatMap((p) => p.rows),
        approvals: per.flatMap((p) => p.approvals),
        tiers: k.ts.map((t) => t.name),
      };
    },
  );

  const prs = createMemo(() => distinctPRs(all()?.rows ?? []));

  return (
    <div class="space-y-4">
      <h1 class="text-2xl font-semibold">PRs</h1>
      <Suspense fallback={<span class="loading loading-dots" />}>
        <Show when={prs().length} fallback={<p class="opacity-60 text-sm">No active PRs.</p>}>
          <div class="rounded-box border border-base-300 overflow-hidden bg-base-200">
            <For each={prs()}>
              {(n) => <PrRow pr={n} rows={all()?.rows ?? []} approvals={all()?.approvals ?? []} tiers={all()?.tiers ?? []} tick={tick()} />}
            </For>
          </div>
        </Show>
      </Suspense>
    </div>
  );
}

function PrRow(props: { pr: number; rows: Tagged[]; approvals: (PendingApproval & { tier: string })[]; tiers: string[]; tick: number }) {
  const forPr = createMemo(() => props.rows.filter((e) => e.pr === props.pr));

  // Identity + per-tier merge, fanned out (first reachable tier for title).
  const [views] = createResource(
    () => (props.tiers.length ? { pr: props.pr, tiers: props.tiers, tick: props.tick } : undefined),
    async (k) => {
      const settled = await Promise.allSettled(k.tiers.map((t) => api.pr(t, k.pr).then((v): [string, PRView] => [t, v])));
      const m = new Map<string, PRView>();
      for (const r of settled) if (r.status === "fulfilled") m.set(r.value[0], r.value[1]);
      return m;
    },
  );
  const title = createMemo(() => [...(views()?.values() ?? [])].find((v) => v.meta)?.meta?.title ?? "");
  const author = createMemo(() => [...(views()?.values() ?? [])].find((v) => v.meta)?.meta?.author_login ?? "");
  const badge = createMemo(() => {
    const vs = [...(views()?.values() ?? [])];
    // Prefer a blocked/automerge signal; fall back to the first tier's view.
    return mergeBadge(vs.find((v) => v.merge?.merge_blocked) ?? vs.find((v) => v.meta?.auto_merge) ?? vs[0]);
  });
  const lastUpdated = createMemo(() => {
    let newest = "";
    for (const e of forPr()) if (e.created_at > newest) newest = e.created_at;
    return relativeTime(newest);
  });

  // Change summary: roll up the newest plan/report execution's stacks per tier.
  const primaryPlanIds = createMemo(() =>
    props.tiers
      .map((t) => primaryExec(forPr().filter((e) => e.tier === t && contextKind(e.context) === "plan")))
      .filter((e): e is Tagged => !!e)
      .map((e) => ({ tier: e.tier, id: e.id })),
  );
  const [details] = createResource(
    () => ({ ids: primaryPlanIds(), tick: props.tick }),
    async (k) => {
      const settled = await Promise.allSettled(k.ids.map((x) => api.execution(x.tier, x.id)));
      return settled.flatMap((r) => (r.status === "fulfilled" ? [r.value] : [])) as ExecutionDetail[];
    },
  );
  const changeSummary = createMemo(() => {
    const stacks: StackState[] = [];
    for (const d of details() ?? []) for (const s of d.graph?.stacks ?? []) stacks.push(s);
    return rollupChangeCounts(stacks);
  });

  return (
    <A href={`/pr/${props.pr}`} class="flex items-center gap-3 px-4 py-3 border-t border-base-300 first:border-t-0 hover:bg-base-100">
      <span class="font-mono font-semibold shrink-0">#{props.pr}</span>
      <Show when={title()}>
        <span class="truncate">{title()}</span>
      </Show>
      <Show when={badge()}>{(b) => <span class="badge badge-ghost badge-sm shrink-0" style={{ color: SEM_DOT[b().sem] }}>{b().label}</span>}</Show>
      <Show when={changeSummary()}>
        <span class="font-mono text-xs opacity-60 shrink-0">{changeSummary()}</span>
      </Show>
      <div class="ml-auto flex items-center gap-4">
        <div class="hidden sm:flex flex-col items-end text-xs opacity-60">
          <Show when={author()}><span>@{author()}</span></Show>
          <Show when={lastUpdated()}><span>{lastUpdated()}</span></Show>
        </div>
        <For each={props.tiers}>
          {(tier) => {
            const stage = () =>
              prLifecycleStage(
                forPr().filter((e) => e.tier === tier),
                props.approvals.filter((a) => a.tier === tier && a.pr === props.pr),
              );
            return (
              <Show when={forPr().some((e) => e.tier === tier)}>
                <span class="inline-flex items-center gap-1.5 text-xs opacity-90">
                  <span class="w-2 h-2 rounded-full" style={{ background: SEM_DOT[stageSem(stage())] }} />
                  <span class="opacity-70">{tier}</span>
                  <span>{STAGE_LABEL[stage()]}</span>
                </span>
              </Show>
            );
          }}
        </For>
      </div>
    </A>
  );
}
