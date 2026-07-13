import { describe, expect, it } from "vitest";
import { poolCapacity, poolConfigured, formatElapsed } from "./pooldata";
import type { InspectPoolSet, InspectPoolSlot } from "./api/client";

const slot = (o: Partial<InspectPoolSlot>): InspectPoolSlot =>
  ({ requester: "sa@fh.com", occupied: false, ...o });
const set = (slots: InspectPoolSlot[]): InspectPoolSet =>
  ({ environment: "nonprod", slots, waiting: [] });

describe("poolCapacity", () => {
  it("counts occupied slots and flags a full pool", () => {
    expect(poolCapacity(set([]))).toEqual({ used: 0, total: 0, full: false });
    expect(poolCapacity(set([slot({}), slot({ occupied: true })]))).toEqual({ used: 1, total: 2, full: false });
    expect(poolCapacity(set([slot({ occupied: true }), slot({ occupied: true })]))).toEqual({ used: 2, total: 2, full: true });
  });
});

describe("poolConfigured", () => {
  it("is true only when the pool has slots", () => {
    expect(poolConfigured(set([]))).toBe(false);
    expect(poolConfigured(set([slot({})]))).toBe(true);
  });
});

describe("formatElapsed", () => {
  it("renders seconds / minutes / hours and guards junk", () => {
    expect(formatElapsed(0)).toBe("0s");
    expect(formatElapsed(45)).toBe("45s");
    expect(formatElapsed(75)).toBe("1m 15s");
    expect(formatElapsed(3600)).toBe("1h 0m");
    expect(formatElapsed(3665)).toBe("1h 1m");
    expect(formatElapsed(-5)).toBe("0s");
  });
});
