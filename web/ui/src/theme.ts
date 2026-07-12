/**
 * theme: the system/light/dark switcher. daisyUI resolves the actual palette
 * from the `data-theme` attribute on <html>; "system" removes the attribute so
 * the prefers-color-scheme-tagged theme applies. Choice persists in localStorage.
 * The pure helpers (resolveDataTheme, normalizeMode) are unit-tested; the thin
 * DOM/storage wrappers are verified at runtime.
 */
const KEY = "tfsp-theme";
export type ThemeMode = "system" | "light" | "dark";

export function resolveDataTheme(mode: ThemeMode): string | null {
  if (mode === "light") return "calm-light";
  if (mode === "dark") return "calm-dark";
  return null;
}

export function normalizeMode(v: string | null): ThemeMode {
  return v === "light" || v === "dark" || v === "system" ? v : "system";
}

export function applyTheme(mode: ThemeMode): void {
  const v = resolveDataTheme(mode);
  const el = document.documentElement;
  if (v) el.setAttribute("data-theme", v);
  else el.removeAttribute("data-theme");
  localStorage.setItem(KEY, mode);
}

export function loadThemeMode(): ThemeMode {
  return normalizeMode(localStorage.getItem(KEY));
}
