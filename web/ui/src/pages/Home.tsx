import { For, Show, Suspense, createResource } from "solid-js";
import { api } from "../api/client";
import { ExecTable } from "../components/ExecTable";
import { ApprovalsTable } from "../components/ApprovalsTable";

/**
 * Home: the cross-tier overview — needs-attention (pending approvals) on top,
 * then each tier's recent executions side by side. Every tier fetch is
 * independent: one unreachable tier renders its own error panel and never
 * blanks the others.
 */
export function Home() {
  const [tiers] = createResource(api.tiers);
  return (
    <div class="space-y-6">
      <Suspense fallback={<span class="loading loading-dots" />}>
        <For each={tiers()}>{(t) => <TierApprovals tier={t.name} />}</For>
        <div class="grid gap-6 lg:grid-cols-2">
          <For each={tiers()}>{(t) => <TierRecent tier={t.name} />}</For>
        </div>
      </Suspense>
    </div>
  );
}

function TierApprovals(props: { tier: string }) {
  const [approvals] = createResource(() => api.approvals(props.tier));
  return (
    <Show when={(approvals() ?? []).length > 0}>
      <section class="card bg-base-100 shadow-sm border-l-4 border-warning">
        <div class="card-body p-4">
          <h2 class="card-title text-base">⏳ awaiting approval — {props.tier}</h2>
          <ApprovalsTable approvals={approvals()!} />
        </div>
      </section>
    </Show>
  );
}

function TierRecent(props: { tier: string }) {
  const [execs] = createResource(() => api.executions(props.tier, { limit: 25 }));
  return (
    <section class="card bg-base-100 shadow-sm">
      <div class="card-body p-4">
        <h2 class="card-title text-base">{props.tier}</h2>
        <Suspense fallback={<span class="loading loading-dots" />}>
          <Show when={!execs.error} fallback={<div class="alert alert-warning text-sm">tier unreachable</div>}>
            <Show when={(execs() ?? []).length > 0} fallback={<p class="opacity-60 text-sm">No executions yet.</p>}>
              <ExecTable tier={props.tier} executions={execs()!} />
            </Show>
          </Show>
        </Suspense>
      </div>
    </section>
  );
}
