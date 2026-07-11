import { useParams } from "@solidjs/router";
import { For, Show, Suspense, createResource } from "solid-js";
import { api } from "../api/client";
import { ExecTable } from "../components/ExecTable";

/**
 * PrView: one PR's full story across every tier — plan/apply/verify runs,
 * newest first, one panel per tier.
 */
export function PrView() {
  const params = useParams();
  const [tiers] = createResource(api.tiers);
  return (
    <div class="space-y-6">
      <h1 class="text-xl font-bold">PR #{params.n}</h1>
      <Suspense fallback={<span class="loading loading-dots" />}>
        <For each={tiers()}>{(t) => <TierTimeline tier={t.name} pr={Number(params.n)} />}</For>
      </Suspense>
    </div>
  );
}

function TierTimeline(props: { tier: string; pr: number }) {
  const [execs] = createResource(() => api.executions(props.tier, { pr: props.pr }));
  return (
    <section class="card bg-base-100 shadow-sm">
      <div class="card-body p-4">
        <h2 class="card-title text-base">{props.tier}</h2>
        <Suspense fallback={<span class="loading loading-dots" />}>
          <Show when={!execs.error} fallback={<div class="alert alert-warning text-sm">tier unreachable</div>}>
            <Show
              when={(execs() ?? []).length > 0}
              fallback={<p class="opacity-60 text-sm">No executions for this PR.</p>}
            >
              <ExecTable tier={props.tier} executions={execs()!} />
            </Show>
          </Show>
        </Suspense>
      </div>
    </section>
  );
}
