/**
 * status: the single mapping from raw runner/serve status strings to the fixed
 * semantic set the whole UI paints with (dot + word, never loud pills). Any new
 * status the serve emits falls through to "idle" so the UI never crashes on it.
 */
export type Sem = "ok" | "waiting" | "failed" | "running" | "idle";

const MAP: Record<string, Sem> = {
  safe: "ok", nochange: "ok", planned: "ok", applied: "ok", success: "ok",
  gated: "waiting", awaiting: "waiting",
  failed: "failed", aborted: "failed", failure: "failed",
  running: "running", initializing: "running", initialized: "running",
  in_progress: "running", moving: "running",
};

export function statusSem(status: string): Sem {
  return MAP[status] ?? "idle";
}

export const SEM_DOT: Record<Sem, string> = {
  ok: "#57c98a", waiting: "#e0b15a", failed: "#e0687a", running: "#6ea8ff", idle: "#8a93a3",
};
export const SEM_LABEL: Record<Sem, string> = {
  ok: "ok", waiting: "waiting", failed: "failed", running: "running", idle: "idle",
};
