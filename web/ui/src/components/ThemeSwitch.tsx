import { createSignal } from "solid-js";
import { applyTheme, loadThemeMode, type ThemeMode } from "../theme";

/** ThemeSwitch: three-state system/light/dark control; writes <html data-theme>. */
export function ThemeSwitch() {
  const [mode, setMode] = createSignal<ThemeMode>(loadThemeMode());
  const pick = (m: ThemeMode) => {
    applyTheme(m);
    setMode(m);
  };
  const modes: [ThemeMode, string][] = [
    ["system", "◐"],
    ["light", "☀"],
    ["dark", "☾"],
  ];
  return (
    <div class="join w-full">
      {modes.map(([m, glyph]) => (
        <button
          class="btn btn-xs join-item flex-1"
          classList={{ "btn-primary": mode() === m, "btn-ghost": mode() !== m }}
          onClick={() => pick(m)}
          aria-label={m}
          title={m}
        >
          {glyph}
        </button>
      ))}
    </div>
  );
}
