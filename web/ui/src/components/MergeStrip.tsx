import { For, Show, createMemo, createResource } from "solid-js";
import { api, type PRMergeState } from "../api/client";
import { SEM_DOT, statusSem } from "../status";

/**
 * MergeStrip: merge-readiness across tiers, mounted under the PR identity
 * header. Shows an automerge on/off chip (from the first tier that has PR
 * meta), one ✓/✗/◷ chip per tier's required check, and a single "what's
 * blocking merge" line when any tier reports merge_blocked. No queue
 * position — that's runner/serve internals, not a reviewer's concern.
 *
 * Graceful degrade: each tier is fetched independently (Promise.allSettled)
 * so an unreachable tier or an old-serve build (no /pr route) just drops out
 * of the strip. If NO tier returns anything, the whole strip renders nothing
 * rather than showing an empty shell.
 */
export function MergeStrip(props: { pr: number; tiers: string[] }) {
  const [rows] = createResource(
    () => (props.tiers.length ? { pr: props.pr, tiers: props.tiers } : undefined),
    async (k) => {
      const settled = await Promise.allSettled(k.tiers.map((tier) => api.pr(tier, k.pr)));
      return settled.flatMap((r) => (r.status === "fulfilled" ? [r.value] : []));
    },
  );

  const autoMerge = createMemo(() => rows()?.find((v) => v.meta)?.meta?.auto_merge);
  const checks = createMemo(() => (rows() ?? []).filter((v) => v.merge?.environment).map((v) => v.merge));
  const blocker = createMemo(() => checks().find((m) => m.merge_blocked && m.blocker)?.blocker);

  return (
    <Show when={rows()?.length}>
      <div class="flex flex-wrap items-center gap-2 text-xs">
        <Show when={autoMerge() !== undefined}>
          <span class="badge badge-ghost badge-sm">automerge {autoMerge() ? "on" : "off"}</span>
        </Show>
        <For each={checks()}>{(m) => <CheckChip merge={m} />}</For>
        <Show when={blocker()}>{(b) => <span class="opacity-70">blocked: {b()}</span>}</Show>
      </div>
    </Show>
  );
}

function CheckChip(props: { merge: PRMergeState }) {
  const sem = createMemo(() => statusSem(props.merge.check_conclusion));
  const glyph = createMemo(() => (sem() === "ok" ? "✓" : sem() === "failed" ? "✗" : "◷"));
  return (
    <span class="badge badge-ghost badge-sm gap-1 font-mono" style={{ color: SEM_DOT[sem()] }}>
      {glyph()} {props.merge.required_check}
    </span>
  );
}
