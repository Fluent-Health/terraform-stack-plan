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
  fillPct?: number; // within-segment progress (only on "now" with real k/N)
}

const CLS: Record<string, string> = {
  done: "bg-success",
  now: "bg-info animate-pulse",
  pending: "bg-base-300",
  failed: "bg-error",
};

// Plain-language descriptions per canonical segment — shown in the tooltip and
// the under-bar caption so "prepare" means something to someone who has never
// read the runner.
export const DESCRIPTIONS: Record<string, string> = {
  prepare: "getting the runner ready (image, provider caches)",
  linting: "static checks over the Terraform modules",
  init: "running terraform init across the changed stacks",
  plan: "computing what would change in each stack",
  classify: "categorising the changes and working out required approvals",
  report: "rendering the reviewer report",
  approve: "waiting for a human to approve the gated changes",
  moves: "moving resources between stack states before the apply",
  apply: "applying the planned changes to real infrastructure",
  verify: "running post-apply verification",
  claim: "acquiring the apply lock",
};

export function describe(key: string): string {
  return DESCRIPTIONS[key] ?? "";
}

// The gate/apply side of the bar; everything else (including passthrough keys
// like linting) renders on the plan side, left of the ┆ divider.
const GATE_SIDE = new Set(["approve", "moves", "apply", "verify"]);

export function stepperSegments(phases: LifecyclePhase[]): Segment[] {
  let dividerPlaced = false;
  return phases.map((p) => {
    const state = (p.state as SegState) ?? "pending";
    const onGateSide = GATE_SIDE.has(p.key) || p.context === "gate" || p.context === "apply" || p.context === "verify";
    const divider = onGateSide && !dividerPlaced;
    if (divider) dividerPlaced = true;
    return {
      key: p.key,
      label: p.label,
      state,
      cls: CLS[state] ?? CLS.pending,
      divider,
      tip: tooltipText(p),
      fillPct: state === "now" && p.progress_pct !== undefined ? p.progress_pct : undefined,
    };
  });
}

// nowCaption: "<label> — <what this means>[ · <sub-phase>][ · <result>]".
export function nowCaption(phases: LifecyclePhase[]): string {
  const now = phases.find((p) => p.state === "now");
  if (!now) return "";
  let cap = now.label;
  const desc = describe(now.key);
  if (desc) cap += ` — ${desc}`;
  if (now.detail) cap += ` · ${now.detail}`;
  if (now.progress_pct !== undefined) cap += ` · ${now.progress_pct}%`;
  if (now.result) cap += ` · ${now.result}`;
  return cap;
}

export function tooltipText(p: LifecyclePhase): string {
  const desc = describe(p.key);
  const withDesc = (head: string) => (desc ? `${head} — ${desc}` : head);
  switch (p.state) {
    case "done":
      return p.result ? `${withDesc(`✓ ${p.label}`)} · ${p.result}` : withDesc(`✓ ${p.label}`);
    case "now": {
      let head = `▸ ${p.label} · running`;
      if (p.detail) head += ` · ${p.detail}`;
      if (p.progress_pct !== undefined) head += ` · ${p.progress_pct}%`;
      const tail = p.result ? ` · ${p.result}` : "";
      return withDesc(head) + tail;
    }
    case "failed":
      return withDesc(`✗ ${p.label} · failed`);
    default:
      return p.result
        ? `${withDesc(`○ ${p.label} · pending`)} · ${p.result}`
        : withDesc(`○ ${p.label} · pending`);
  }
}

export type LifecycleStage =
  | "planning"
  | "planned"
  | "awaiting approval"
  | "applying"
  | "applied"
  | "verifying"
  | "failed"
  | "";

// stageFromLifecycle: the tier's headline stage, derived purely from the
// folded phases — prominent enough that "is this planning or applying?" never
// needs squinting at segments.
export function stageFromLifecycle(phases: LifecyclePhase[]): LifecycleStage {
  if (phases.length === 0) return "";
  if (phases.some((p) => p.state === "failed")) return "failed";
  const now = phases.find((p) => p.state === "now");
  if (now) {
    if (now.key === "apply" || now.key === "moves") return "applying";
    if (now.key === "verify") return "verifying";
    if (now.key === "approve") return "awaiting approval";
    return "planning";
  }
  const approve = phases.find((p) => p.key === "approve");
  if (approve && approve.state === "pending") return "awaiting approval";
  const apply = phases.find((p) => p.key === "apply");
  if (apply) {
    return apply.state === "done" ? "applied" : "applying";
  }
  // Plan side only: all done → planned, anything pending → planning.
  return phases.every((p) => p.state === "done") ? "planned" : "planning";
}
