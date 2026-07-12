// Thin typed client over the UI backend. Payload shapes come from the tier
// contract (api/openapi.yaml → tier-schema.d.ts via `yarn gen:types`) — the
// UI backend proxies tier responses verbatim, so those types ARE the wire.
import type { components } from "./tier-schema";

export type ExecutionSummary = components["schemas"]["ExecutionSummary"];
export type PendingApproval = components["schemas"]["PendingApproval"];
export type ExecutionDetail = components["schemas"]["ExecutionDetail"];
export type StackState = components["schemas"]["StackState"];
export type StoredGateTarget = components["schemas"]["StoredGateTarget"];
export type PRView = components["schemas"]["PRView"];
export type PRMeta = components["schemas"]["PRMeta"];
export type PRMergeState = components["schemas"]["PRMergeState"];

export interface Me {
  email: string;
}
export interface TierInfo {
  name: string;
  url: string;
}

/** Thrown on 401 — the session is missing/expired; the shell shows login. */
export class Unauthorized extends Error {
  constructor() {
    super("unauthorized");
  }
}

async function j<T>(url: string): Promise<T> {
  const r = await fetch(url);
  if (r.status === 401) throw new Unauthorized();
  if (!r.ok) throw new Error(`${r.status}: ${await r.text()}`);
  return r.json() as Promise<T>;
}

export const api = {
  me: () => j<Me>("/api/me"),
  tiers: () => j<TierInfo[]>("/api/tiers"),
  executions: (tier: string, opts: { pr?: number; limit?: number } = {}) => {
    const q = new URLSearchParams();
    if (opts.pr !== undefined) q.set("pr", String(opts.pr));
    if (opts.limit !== undefined) q.set("limit", String(opts.limit));
    const qs = q.toString();
    return j<ExecutionSummary[]>(`/api/tiers/${tier}/executions${qs ? "?" + qs : ""}`);
  },
  approvals: (tier: string) => j<PendingApproval[]>(`/api/tiers/${tier}/approvals`),
  execution: (tier: string, id: string) => j<ExecutionDetail>(`/api/tiers/${tier}/executions/${id}`),
  pr: (tier: string, n: number) => j<PRView>(`/api/tiers/${tier}/pr/${n}`),
  planFragment: async (tier: string, exec: string, stack: string): Promise<string> => {
    const r = await fetch(`/api/tiers/${tier}/plan/${exec}/${stack}`);
    if (r.status === 401) throw new Unauthorized();
    if (!r.ok) throw new Error(`${r.status}: ${await r.text()}`);
    return r.text();
  },
  logout: () => fetch("/auth/logout", { method: "POST" }),
};

/** executionEventsURL is the SSE change stream for one execution. */
export const executionEventsURL = (tier: string, id: string) => `/api/tiers/${tier}/executions/${id}/events`;

/** logFollowURL is the SSE live log stream for one stack. */
export const logFollowURL = (tier: string, exec: string, stack: string) =>
  `/api/tiers/${tier}/logs/${exec}/${stack}?follow=1`;

export const loginURL = () => `/auth/login?next=${encodeURIComponent(location.pathname + location.search)}`;
