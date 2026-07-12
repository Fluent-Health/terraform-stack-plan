import { Show, createResource } from "solid-js";
import { api, type PRMeta } from "../api/client";

/**
 * PrIdentity: the PR's identity header — title, description, author, branch,
 * and a GitHub link. Identity is tier-agnostic (same PR, same GitHub source),
 * so we ask each reachable tier in turn and use the first that has it.
 *
 * Graceful degrade: old serve builds (no /pr meta), a tier that 404s, or a
 * tier that's simply unreachable all fall through to the next tier; if none
 * of them return meta, we render today's minimal "#{n}" header instead of
 * blanking or erroring the page.
 */
export function PrIdentity(props: { pr: number; tiers: string[] }) {
  const [meta] = createResource(
    () => (props.tiers.length ? { pr: props.pr, tiers: props.tiers } : undefined),
    async (k): Promise<PRMeta | undefined> => {
      for (const tier of k.tiers) {
        try {
          const view = await api.pr(tier, k.pr);
          if (view.meta) return view.meta;
        } catch {
          // unreachable tier or old-serve 404 — try the next tier.
        }
      }
      return undefined;
    },
  );

  return (
    <header class="space-y-1">
      <Show
        when={meta()}
        fallback={
          <div class="flex items-baseline gap-3">
            <h1 class="text-2xl font-semibold font-mono">#{props.pr}</h1>
            <span class="text-sm opacity-60">this PR across every tier</span>
          </div>
        }
      >
        {(m) => (
          <>
            <div class="flex items-baseline gap-3 flex-wrap">
              <h1 class="text-2xl font-semibold">
                <span class="font-mono">#{props.pr}</span> {m().title}
              </h1>
              <Show when={m().url}>
                <a class="link link-hover text-sm shrink-0" target="_blank" rel="noreferrer" href={m().url}>
                  GitHub ↗
                </a>
              </Show>
            </div>
            <Show when={m().body}>
              <p class="text-sm opacity-70 line-clamp-2">{m().body}</p>
            </Show>
            <div class="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs opacity-60">
              <Show when={m().author_login}>
                <span>@{m().author_login}</span>
              </Show>
              <Show when={m().head_ref}>
                <span class="font-mono">{m().head_ref}</span>
              </Show>
            </div>
          </>
        )}
      </Show>
    </header>
  );
}
