// web/ui/src/pages/Ops.tsx
import { For, Show, Suspense, createEffect, createMemo, createResource } from "solid-js";
import { A } from "@solidjs/router";
import { api, type ExecutionSummary } from "../api/client";
import { ApprovalsTable } from "../components/ApprovalsTable";
import { statusSem } from "../status";

/**
 * Ops: the debug surface — what's stuck. Plan 1 shows errored runs + pending
 * approvals from existing endpoints; the applier-slot pool panel arrives in Plan 3.
 */
export function Ops() {
  createEffect(() => {
    document.title = "Ops board · tfstackplan";
  });
  const [tiers] = createResource(api.tiers);
  return (
    <div class="space-y-5">
      <h1 class="text-2xl font-semibold">Ops board</h1>

      <section class="card bg-base-200 border border-base-300">
        <div class="card-body p-4">
          <h2 class="card-title text-base">🔑 Applier slots</h2>
          <p class="opacity-50 text-sm">Pool enumeration + occupancy lands in Plan 3 (needs a serve read endpoint).</p>
        </div>
      </section>

      <Suspense fallback={<span class="loading loading-dots" />}>
        <section class="card bg-base-200 border border-base-300">
          <div class="card-body p-4">
            <h2 class="card-title text-base">✕ Errored — needs a human</h2>
            <For each={tiers()}>{(t) => <ErroredList tier={t.name} />}</For>
          </div>
        </section>
        <section class="card bg-base-200 border border-base-300">
          <div class="card-body p-4">
            <h2 class="card-title text-base">⏳ Waiting on approval</h2>
            <For each={tiers()}>{(t) => <ApprovalList tier={t.name} />}</For>
          </div>
        </section>
      </Suspense>
    </div>
  );
}

function ErroredList(props: { tier: string }) {
  const [execs] = createResource(() => api.executions(props.tier, { limit: 100 }));
  const failed = createMemo(() =>
    (execs() ?? []).filter((e: ExecutionSummary) => !e.superseded_by && statusSem(e.status) === "failed"),
  );
  return (
    <Show
      when={!execs.error}
      fallback={<div class="alert alert-warning text-sm">{props.tier} unreachable</div>}
    >
      <Show when={failed().length}>
        <For each={failed()}>
          {(e) => (
            <A href={`/pr/${e.pr}`} class="flex items-center gap-3 py-2 text-sm hover:bg-base-100 rounded px-2">
              <span class="w-2 h-2 rounded-full bg-error" />
              <span class="font-mono font-semibold">#{e.pr}</span>
              <span class="opacity-70 font-mono text-xs">{e.context}</span>
              <span class="ml-auto opacity-50 text-xs">{new Date(e.created_at).toLocaleTimeString()}</span>
            </A>
          )}
        </For>
      </Show>
    </Show>
  );
}

function ApprovalList(props: { tier: string }) {
  const [approvals, { refetch }] = createResource(() => api.approvals(props.tier));
  return (
    <Show
      when={!approvals.error}
      fallback={<div class="alert alert-warning text-sm">{props.tier} unreachable</div>}
    >
      <Show when={approvals()?.length}>
        <div class="text-xs opacity-60 mt-1">{props.tier}</div>
        <ApprovalsTable tier={props.tier} approvals={approvals()!} onDecided={refetch} />
      </Show>
    </Show>
  );
}
