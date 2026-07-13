// web/ui/src/pages/Ops.tsx
import { For, Show, Suspense, createEffect, createMemo, createResource } from "solid-js";
import { A } from "@solidjs/router";
import { api, type ExecutionSummary, type InspectPoolSet, type InspectPoolSlot, type InspectPoolWaitingPR } from "../api/client";
import { ApprovalsTable } from "../components/ApprovalsTable";
import { poolCapacity, poolConfigured, formatElapsed } from "../pooldata";
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

      <Suspense fallback={<span class="loading loading-dots" />}>
        <section class="card bg-base-200 border border-base-300">
          <div class="card-body p-4">
            <h2 class="card-title text-base">🔑 Applier slots</h2>
            <For each={tiers()}>{(t) => <PoolPanel tier={t.name} />}</For>
          </div>
        </section>
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

function PoolPanel(props: { tier: string }) {
  const [pool] = createResource(() => api.pool(props.tier));
  return (
    <Show
      when={!pool.error}
      fallback={<div class="alert alert-warning text-sm">{props.tier} unreachable</div>}
    >
      <Show when={pool()} fallback={<span class="loading loading-dots loading-sm" />}>
        {(set) => <PoolBody tier={props.tier} set={set()} />}
      </Show>
    </Show>
  );
}

function PoolBody(props: { tier: string; set: InspectPoolSet }) {
  const cap = createMemo(() => poolCapacity(props.set));
  return (
    <div class="space-y-2">
      <div class="flex items-center gap-2">
        <span class="text-xs opacity-60 font-mono">{props.tier}</span>
        <Show
          when={poolConfigured(props.set)}
          fallback={<span class="text-sm opacity-50">no pool configured</span>}
        >
          <span
            class="badge badge-sm"
            classList={{ "badge-error": cap().full, "badge-ghost": !cap().full }}
          >
            {cap().used} / {cap().total} used
          </span>
        </Show>
      </div>
      <Show when={poolConfigured(props.set)}>
        <div class="grid gap-2 sm:grid-cols-2">
          <For each={props.set.slots}>{(slot) => <SlotCard slot={slot} />}</For>
        </div>
        <Show when={props.set.waiting.length}>
          <div class="mt-2 space-y-1">
            <div class="text-xs opacity-60">Waiting for a slot</div>
            <For each={props.set.waiting}>{(w) => <WaitingRow waiting={w} />}</For>
          </div>
        </Show>
      </Show>
    </div>
  );
}

function SlotCard(props: { slot: InspectPoolSlot }) {
  return (
    <Show
      when={props.slot.occupied}
      fallback={
        <div class="border border-dashed border-base-300 rounded px-3 py-2 text-sm opacity-40">
          <div class="font-mono text-xs truncate">{props.slot.requester}</div>
          <div>free</div>
        </div>
      }
    >
      <A href={`/pr/${props.slot.pr}`} class="block border border-base-300 bg-base-100 rounded px-3 py-2 text-sm hover:bg-base-300">
        <div class="flex items-center gap-2">
          <span class="w-2 h-2 rounded-full bg-info" />
          <span class="font-mono font-semibold">#{props.slot.pr}</span>
          <Show when={props.slot.elapsed_seconds !== undefined}>
            <span class="ml-auto opacity-60 text-xs">{formatElapsed(props.slot.elapsed_seconds!)}</span>
          </Show>
        </div>
        <div class="opacity-70 text-xs truncate">
          {props.slot.environment} · {props.slot.grant_name} · {props.slot.state}
        </div>
        <div class="font-mono text-[10px] opacity-40 truncate">{props.slot.requester}</div>
      </A>
    </Show>
  );
}

function WaitingRow(props: { waiting: InspectPoolWaitingPR }) {
  return (
    <A href={`/pr/${props.waiting.pr}`} class="flex items-center gap-2 text-sm py-1 px-2 rounded hover:bg-base-100">
      <span class="w-2 h-2 rounded-full bg-warning" />
      <span class="font-mono font-semibold">#{props.waiting.pr}</span>
      <span class="opacity-60 text-xs">{props.waiting.reason}</span>
      <span class="ml-auto opacity-50 text-xs">
        blocked by <span class="font-mono">#{props.waiting.blocker_pr}</span> ({props.waiting.blocker_env})
      </span>
    </A>
  );
}
