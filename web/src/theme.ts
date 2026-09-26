export type ThemeChoice = "system" | "light" | "dark";

const KEY = "kmdn-theme";

export function storedTheme(): ThemeChoice {
  try {
    const v = localStorage.getItem(KEY);
    if (v === "light" || v === "dark" || v === "system") return v;
  } catch {
    /* storage unavailable */
  }
  return "system";
}

export function resolveTheme(choice: ThemeChoice, prefersDark: boolean): "light" | "dark" {
  if (choice === "system") return prefersDark ? "dark" : "light";
  return choice;
}

export function applyTheme(choice: ThemeChoice) {
  const mq = window.matchMedia?.("(prefers-color-scheme: dark)");
  document.documentElement.dataset.theme = resolveTheme(choice, !!mq?.matches);
  try {
    localStorage.setItem(KEY, choice);
  } catch {
    /* ignore */
  }
}
