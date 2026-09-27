export type ThemeChoice = "system" | "light" | "dark";
export type Palette = "default" | "catppuccin" | "gruvbox" | "base16" | "nord";
export type UIScale = 100 | 110 | 120 | 135;

export const PALETTES: Palette[] = ["default", "catppuccin", "gruvbox", "base16", "nord"];
export const UI_SCALES: UIScale[] = [100, 110, 120, 135];

/** A person's appearance: light/dark mode, color palette, interface size. */
export type Appearance = { mode: ThemeChoice; palette: Palette; scale: UIScale };

export const DEFAULT_APPEARANCE: Appearance = { mode: "system", palette: "default", scale: 120 };

const KEY = "kmdn-appearance";
const LEGACY_KEY = "kmdn-theme"; // the mode alone, before palettes

/** Validates values from storage or the API, filling in defaults. */
export function normalizeAppearance(a: Partial<Record<keyof Appearance, unknown>>): Appearance {
  const mode = a.mode === "light" || a.mode === "dark" || a.mode === "system" ? a.mode : DEFAULT_APPEARANCE.mode;
  const palette = PALETTES.includes(a.palette as Palette) ? (a.palette as Palette) : DEFAULT_APPEARANCE.palette;
  const scale = UI_SCALES.includes(a.scale as UIScale) ? (a.scale as UIScale) : DEFAULT_APPEARANCE.scale;
  return { mode, palette, scale };
}

/** The appearance cached in this browser (the account is the source of truth once signed in). */
export function storedAppearance(): Appearance {
  try {
    const v = localStorage.getItem(KEY);
    if (v) return normalizeAppearance(JSON.parse(v) as Partial<Appearance>);
    return normalizeAppearance({ mode: localStorage.getItem(LEGACY_KEY) ?? undefined });
  } catch {
    return DEFAULT_APPEARANCE;
  }
}

export function resolveTheme(choice: ThemeChoice, prefersDark: boolean): "light" | "dark" {
  if (choice === "system") return prefersDark ? "dark" : "light";
  return choice;
}

/** Applies an appearance to the document (data-theme, data-palette, --ui-scale) and caches it. */
export function applyAppearance(a: Appearance) {
  const mq = window.matchMedia?.("(prefers-color-scheme: dark)");
  const root = document.documentElement;
  root.dataset.theme = resolveTheme(a.mode, !!mq?.matches);
  if (a.palette === "default") delete root.dataset.palette;
  else root.dataset.palette = a.palette;
  root.style.setProperty("--ui-scale", String(a.scale / 100));
  try {
    localStorage.setItem(KEY, JSON.stringify(a));
  } catch {
    /* ignore */
  }
}
