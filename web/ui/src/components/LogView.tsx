import { createEffect, onCleanup } from "solid-js";
import { LineBuffer } from "../ansi";
import { logFollowURL } from "../api/client";

/**
 * LogView streams one stack's log over the SSE proxy and appends in place —
 * no reloads, no re-render of existing lines. The EventSource resumes via
 * Last-Event-ID (byte offsets) on reconnect; the server ends finished logs
 * with an `event: done`, which closes the stream instead of auto-retrying.
 * Lines are ANSI-rendered to sanitized HTML (everything is escaped first).
 */
export function LogView(props: { tier: string; exec: string; stack: string }) {
  let pre!: HTMLPreElement;

  createEffect(() => {
    const url = logFollowURL(props.tier, props.exec, props.stack);
    pre.innerHTML = "";
    const completedEl = document.createElement("div");
    const pendingEl = document.createElement("div");
    pre.append(completedEl, pendingEl);
    const buf = new LineBuffer();
    const es = new EventSource(url);
    es.onmessage = (e) => {
      // One SSE message carries one chunk; data lines join with \n and the
      // chunk's trailing newline is not encoded, so re-add it.
      const { completed, pending } = buf.push(e.data + "\n");
      for (const line of completed) {
        const div = document.createElement("div");
        div.innerHTML = line || " ";
        completedEl.append(div);
      }
      pendingEl.innerHTML = pending;
      pre.scrollTop = pre.scrollHeight;
    };
    es.addEventListener("done", () => es.close());
    es.onerror = () => {
      // EventSource retries on transient errors by itself (with Last-Event-ID);
      // nothing to do — `done` is the deliberate end.
    };
    onCleanup(() => es.close());
  });

  return (
    <pre
      ref={pre}
      class="bg-neutral text-neutral-content text-xs leading-relaxed p-3 rounded-box overflow-auto max-h-[32rem] whitespace-pre-wrap"
    />
  );
}
