import { For, Show, Suspense, createMemo, createResource } from "solid-js";
import { useParams } from "@solidjs/router";
import { api } from "../api/client";
import { primaryExec, latestPerContext } from "../prdata";
import { TierPanel } from "../components/TierPanel";

/**
 * PrView: the hero. One PR across both tiers, newest run per tier side-by-side.
 * Identity is minimal in Plan 1 (PR# only); title/description/author and
 * the merge strip arrive in Plan 2 when serve exposes PR metadata.
 */
export function PrView() {
  const params = useParams();
  const [tiers] = createResource(api.tiers);
  return (
    <div class="space-y-5">
      <header class="flex items-baseline gap-3">
        <h1 class="text-2xl font-semibold">#{params.n}</h1>
        <span class="text-sm opacity-60">this PR across every tier</span>
      </header>
      <Suspense fallback={<span class="loading loading-dots" />}>
        <div class="grid gap-4 lg:grid-cols-2">
          <For each={tiers()}>{(t) => <TierColumn tier={t.name} pr={Number(params.n)} />}</For>
        </div>
      </Suspense>
    </div>
  );
}

function TierColumn(props: { tier: string; pr: number }) {
  const [execs, { refetch }] = createResource(() => api.executions(props.tier, { pr: props.pr }));
  const primary = createMemo(() => primaryExec(execs() ?? []));
  const contexts = createMemo(() => latestPerContext(execs() ?? []));
  return (
    <Show when={!execs.error} fallback={<div class="alert alert-warning text-sm">{props.tier} unreachable</div>}>
      <Suspense fallback={<span class="loading loading-dots loading-sm" />}>
        <Show when={primary()} fallback={<p class="opacity-60 text-sm">No run on {props.tier}.</p>}>
          {(p) => <TierPanel tier={props.tier} summary={p()} contexts={contexts()} onSuperseded={refetch} />}
        </Show>
      </Suspense>
    </Show>
  );
}
