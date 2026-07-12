import { For, Show, Suspense, createMemo, createResource } from "solid-js";
import { useParams } from "@solidjs/router";
import { api } from "../api/client";
import { primaryExec, latestPerContext } from "../prdata";
import { TierPanel } from "../components/TierPanel";
import { PrIdentity } from "../components/PrIdentity";
import { MergeStrip } from "../components/MergeStrip";

/**
 * PrView: the hero. One PR across both tiers, newest run per tier side-by-side.
 * The identity header (title/author/branch/GitHub link) is fetched
 * separately by PrIdentity, which degrades to a minimal "#{n}" header when
 * no reachable tier has PR metadata.
 */
export function PrView() {
  const params = useParams();
  const [tiers] = createResource(api.tiers);
  return (
    <div class="space-y-5">
      <PrIdentity pr={Number(params.n)} tiers={(tiers() ?? []).map((t) => t.name)} />
      <MergeStrip pr={Number(params.n)} tiers={(tiers() ?? []).map((t) => t.name)} />
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
