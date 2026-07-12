/**
 * stepper: the canonical run-stage vocabulary and the pure mapping from a
 * (phase, execution-status) pair to a per-stage visual state. Segments carry
 * state only — the label lives in the caption + tooltips (never inline), so any
 * number of stages fits. Unknown phases resolve to all-pending (forward compat).
 */
import { statusSem } from "./status";

export const STAGES = ["queued", "image", "init", "plan", "classify", "apply", "verify"] as const;
export type StageState = "done" | "now" | "pending" | "failed";

export function stageStates(phase: string, execStatus: string): StageState[] {
  const sem = statusSem(execStatus);
  if (sem === "ok") return STAGES.map(() => "done"); // finished clean → all done
  const cur = (STAGES as readonly string[]).indexOf(phase);
  return STAGES.map((_, i) => {
    if (cur < 0) return "pending"; // unknown phase
    if (i < cur) return "done";
    if (i > cur) return "pending";
    return sem === "failed" ? "failed" : "now";
  });
}
