/**
 * pooldata: pure display math for the Ops-board applier-slot panel. No I/O —
 * callers pass an already-fetched InspectPoolSet (the tier contract shape).
 */
import type { InspectPoolSet } from "./api/client";

/** used = occupied slots, total = configured slots; full only when a non-empty pool has every slot occupied. */
export function poolCapacity(set: InspectPoolSet): { used: number; total: number; full: boolean } {
  const total = set.slots.length;
  const used = set.slots.filter((s) => s.occupied).length;
  return { used, total, full: total > 0 && used >= total };
}

/** A pool is "configured" only when the tier reports slots; empty slots → "no pool configured". */
export function poolConfigured(set: InspectPoolSet): boolean {
  return set.slots.length > 0;
}

/** Compact elapsed rendering from elapsed_seconds: "45s", "1m 15s", "1h 1m". Junk/negative → "0s". */
export function formatElapsed(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 0) return "0s";
  const h = Math.floor(seconds / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  const s = Math.floor(seconds % 60);
  if (h > 0) return `${h}h ${m}m`;
  if (m > 0) return `${m}m ${s}s`;
  return `${s}s`;
}
