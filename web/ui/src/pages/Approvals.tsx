import { For, Suspense, createResource } from "solid-js";
import { api } from "../api/client";
import { ApprovalsTable } from "../components/ApprovalsTable";

/** Approvals: every gate target awaiting human action, across all tiers. */
export function Approvals() {
  const [tiers] = createResource(api.tiers);
  return (
    <div class="space-y-6">
      <h1 class="text-xl font-bold">Approvals</h1>
      <Suspense fallback={<span class="loading loading-dots" />}>
        <For each={tiers()}>{(t) => <TierApprovals tier={t.name} />}</For>
      </Suspense>
    </div>
  );
}

function TierApprovals(props: { tier: string }) {
  const [approvals] = createResource(() => api.approvals(props.tier));
  return (
    <section class="card bg-base-100 shadow-sm">
      <div class="card-body p-4">
        <h2 class="card-title text-base">{props.tier}</h2>
        <Suspense fallback={<span class="loading loading-dots" />}>
          <ApprovalsTable approvals={approvals() ?? []} />
        </Suspense>
      </div>
    </section>
  );
}
