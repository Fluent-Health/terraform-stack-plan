import { For, Show, Suspense, createMemo, createResource } from "solid-js";
import { A } from "@solidjs/router";
import { api, type ExecutionSummary } from "../api/client";
import { distinctPRs, latestPerContext } from "../prdata";
import { SEM_DOT, statusSem } from "../status";

/** Prs: the landing list of active PRs, newest-first, each a link into the hero. */
export function Prs() {
  const [tiers] = createResource(api.tiers);
  const [all] = createResource(tiers, async (ts) => {
    const per = await Promise.all(
      ts.map((t) => api.executions(t.name, { limit: 200 }).catch(() => [] as ExecutionSummary[])),
    );
    return per.flat();
  });
  const prs = createMemo(() => distinctPRs(all() ?? []));
  return (
    <div class="space-y-4">
      <h1 class="text-2xl font-semibold">PRs</h1>
      <Suspense fallback={<span class="loading loading-dots" />}>
        <Show when={prs().length} fallback={<p class="opacity-60 text-sm">No active PRs.</p>}>
          <div class="rounded-box border border-base-300 overflow-hidden bg-base-200">
            <For each={prs()}>
              {(n) => {
                const runs = () => latestPerContext((all() ?? []).filter((e) => e.pr === n));
                return (
                  <A href={`/pr/${n}`} class="flex items-center gap-3 px-4 py-3 border-t border-base-300 first:border-t-0 hover:bg-base-100">
                    <span class="font-mono font-semibold">#{n}</span>
                    <div class="ml-auto flex items-center gap-3">
                      <For each={runs()}>
                        {(r) => (
                          <span class="inline-flex items-center gap-1 text-xs opacity-80">
                            <span class="w-2 h-2 rounded-full" style={{ background: SEM_DOT[statusSem(r.status)] }} />
                            {r.context}
                          </span>
                        )}
                      </For>
                    </div>
                  </A>
                );
              }}
            </For>
          </div>
        </Show>
      </Suspense>
    </div>
  );
}
