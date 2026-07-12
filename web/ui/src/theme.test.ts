import { describe, expect, it } from "vitest";
import { resolveDataTheme, normalizeMode } from "./theme";

describe("resolveDataTheme", () => {
  it("maps modes to data-theme attribute values", () => {
    expect(resolveDataTheme("light")).toBe("calm-light");
    expect(resolveDataTheme("dark")).toBe("calm-dark");
    expect(resolveDataTheme("system")).toBeNull(); // let prefers-color-scheme decide
  });
});

describe("normalizeMode", () => {
  it("passes through valid modes", () => {
    expect(normalizeMode("light")).toBe("light");
    expect(normalizeMode("dark")).toBe("dark");
    expect(normalizeMode("system")).toBe("system");
  });
  it("defaults to system on null/garbage", () => {
    expect(normalizeMode(null)).toBe("system");
    expect(normalizeMode("nonsense")).toBe("system");
  });
});
