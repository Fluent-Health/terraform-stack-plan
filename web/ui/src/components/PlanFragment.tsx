import { Show, createResource } from "solid-js";
import { api } from "../api/client";

/**
 * PlanFragment shows one stack's rendered plan diff — HTML rendered by the
 * tier serve itself (trusted, server-escaped output of our own renderer) and
 * injected as-is, so the SPA never re-implements the diff renderer.
 */
export function PlanFragment(props: { tier: string; exec: string; stack: string }) {
  const [html] = createResource(
    () => ({ ...props }),
    (k) => api.planFragment(k.tier, k.exec, k.stack),
  );
  return (
    <Show
      when={!html.error}
      fallback={<p class="opacity-60 text-sm">No plan section for this stack (apply runs store none).</p>}
    >
      <div class="prose prose-sm max-w-none overflow-auto" innerHTML={html()} />
    </Show>
  );
}
