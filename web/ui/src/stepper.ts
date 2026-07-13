/**
 * stepper: pure reshaping of a folded lifecycle timeline into thin colour-only
 * segments + a "happening now" caption + per-segment tooltip text. No I/O, no
 * DOM — the LifecycleStepper component renders these.
 */
import type { LifecyclePhase } from "./api/client";

export type SegState = "done" | "now" | "pending" | "failed";

export interface Segment {
  key: string;
  label: string;
  state: SegState;
  cls: string; // Tailwind/daisyUI colour class for the bar
  divider: boolean; // render a ┆ before this segment (plan-side → gate/apply-side)
  tip: string;
}

const CLS: Record<string, string> = {
  done: "bg-success",
  now: "bg-info animate-pulse",
  pending: "bg-base-300",
  failed: "bg-error",
};

// plan-side keys precede the ┆; everything else is on the gate/apply/verify side.
const PLAN_SIDE = new Set(["prepare", "init", "moves", "plan", "classify", "report"]);

export function stepperSegments(phases: LifecyclePhase[]): Segment[] {
  let dividerPlaced = false;
  return phases.map((p) => {
    const state = (p.state as SegState) ?? "pending";
    const onGateSide = !PLAN_SIDE.has(p.key);
    const divider = onGateSide && !dividerPlaced;
    if (divider) dividerPlaced = true;
    return {
      key: p.key,
      label: p.label,
      state,
      cls: CLS[state] ?? CLS.pending,
      divider,
      tip: tooltipText(p),
    };
  });
}

export function nowCaption(phases: LifecyclePhase[]): string {
  const now = phases.find((p) => p.state === "now");
  if (!now) return "";
  return now.result ? `${now.label} · ${now.result}` : now.label;
}

export function tooltipText(p: LifecyclePhase): string {
  switch (p.state) {
    case "done":
      return p.result ? `✓ ${p.label} · ${p.result}` : `✓ ${p.label}`;
    case "now":
      return p.result ? `▸ ${p.label} · running · ${p.result}` : `▸ ${p.label} · running`;
    case "failed":
      return `✗ ${p.label} · failed`;
    default:
      return p.result ? `○ ${p.label} · pending · ${p.result}` : `○ ${p.label} · pending`;
  }
}
