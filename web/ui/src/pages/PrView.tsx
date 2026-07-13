import { For, Show, Suspense, createMemo, createResource } from "solid-js";
import { useParams, useSearchParams } from "@solidjs/router";
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
  const [searchParams, setSearchParams] = useSearchParams();
  const activeTier = () => searchParams.tier;

  return (
    <div class="space-y-5">
      <PrIdentity pr={Number(params.n)} tiers={(tiers() ?? []).map((t) => t.name)} />
      <MergeStrip pr={Number(params.n)} tiers={(tiers() ?? []).map((t) => t.name)} />
      
      <Suspense fallback={<span class="loading loading-dots" />}>
        <Show when={(tiers() ?? []).length > 0}>
          <div class="flex gap-2">
            <button
              class="btn btn-xs sm:btn-sm rounded-full"
              classList={{ "btn-primary": !activeTier(), "btn-ghost border border-base-300": !!activeTier() }}
              onClick={() => setSearchParams({ tier: undefined })}
            >
              All Tiers
            </button>
            <For each={tiers()}>
              {(t) => (
                <button
                  class="btn btn-xs sm:btn-sm rounded-full"
                  classList={{ "btn-primary": activeTier() === t.name, "btn-ghost border border-base-300": activeTier() !== t.name }}
                  onClick={() => setSearchParams({ tier: t.name })}
                >
                  {t.name}
                </button>
              )}
            </For>
          </div>
        </Show>
      </Suspense>

      <Suspense fallback={<span class="loading loading-dots" />}>
        <div
          class="grid gap-4"
          classList={{
            "lg:grid-cols-2": !activeTier(),
            "grid-cols-1": !!activeTier(),
          }}
        >
          <For each={tiers()}>
            {(t) => (
              <div
                class="w-full"
                classList={{ hidden: !!activeTier() && activeTier() !== t.name }}
              >
                <TierColumn tier={t.name} pr={Number(params.n)} />
              </div>
            )}
          </For>
        </div>
      </Suspense>
    </div>
  );
}

function TierColumn(props: { tier: string; pr: number }) {
  const [execs, { refetch }] = createResource(() => api.executions(props.tier, { pr: props.pr }));
  const primary = createMemo(() => primaryExec(execs.latest ?? []));
  const contexts = createMemo(() => latestPerContext(execs.latest ?? []));
  return (
    <Show when={!execs.error} fallback={<div class="alert alert-warning text-sm">{props.tier} unreachable</div>}>
      <Show when={execs.latest} fallback={<span class="loading loading-dots loading-sm" />}>
        <Show when={primary()} fallback={<p class="opacity-60 text-sm">No run on {props.tier}.</p>}>
          {(p) => <TierPanel tier={props.tier} summary={p()} contexts={contexts()} onSuperseded={refetch} />}
        </Show>
      </Show>
    </Show>
  );
}
