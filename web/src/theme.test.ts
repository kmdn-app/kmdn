import { applyAppearance, normalizeAppearance, resolveTheme, storedAppearance } from "./theme";

test("resolveTheme follows the system only for 'system'", () => {
  expect(resolveTheme("system", true)).toBe("dark");
  expect(resolveTheme("system", false)).toBe("light");
  expect(resolveTheme("light", true)).toBe("light");
  expect(resolveTheme("dark", false)).toBe("dark");
});

test("normalizeAppearance fills defaults and drops unknown values", () => {
  expect(normalizeAppearance({})).toEqual({ mode: "system", palette: "default", scale: 120 });
  expect(normalizeAppearance({ mode: "dark", palette: "nord", scale: 135 })).toEqual({ mode: "dark", palette: "nord", scale: 135 });
  expect(normalizeAppearance({ mode: "sepia", palette: "solarized", scale: 300 })).toEqual({ mode: "system", palette: "default", scale: 120 });
});

test("applyAppearance sets the mode, palette and size on the document", () => {
  applyAppearance({ mode: "dark", palette: "gruvbox", scale: 110 });
  const root = document.documentElement;
  expect(root.dataset.theme).toBe("dark");
  expect(root.dataset.palette).toBe("gruvbox");
  expect(root.style.getPropertyValue("--ui-scale")).toBe("1.1");
  expect(storedAppearance()).toEqual({ mode: "dark", palette: "gruvbox", scale: 110 });
  applyAppearance({ mode: "light", palette: "default", scale: 120 });
  expect(root.dataset.palette).toBeUndefined();
});
