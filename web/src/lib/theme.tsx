import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { api, useMe } from "@/lib/api";
import { applyAppearance, DEFAULT_APPEARANCE, normalizeAppearance, resolveTheme, storedAppearance, type Appearance, type Palette, type ThemeChoice, type UIScale } from "@/theme";

const sameAppearance = (a: Appearance, b: Appearance) => a.mode === b.mode && a.palette === b.palette && a.scale === b.scale;

type ThemeState = {
  choice: ThemeChoice;
  resolved: "light" | "dark";
  palette: Palette;
  scale: UIScale;
  setChoice: (c: ThemeChoice) => void;
  setPalette: (p: Palette) => void;
  setScale: (s: UIScale) => void;
};

const ThemeContext = createContext<ThemeState | null>(null);

function prefersDark() {
  return typeof window !== "undefined" && !!window.matchMedia?.("(prefers-color-scheme: dark)").matches;
}

/**
 * Appearance per person (docs/specs/02-ux.md#visual-language): mode,
 * palette and interface size live on the account, so they follow you to
 * any device; this browser keeps a copy so pages load without a flash.
 */
export function ThemeProvider({ children }: { children: ReactNode }) {
  const [appearance, setAppearance] = useState<Appearance>(storedAppearance);
  const [dark, setDark] = useState(prefersDark);
  const { data: me } = useMe();
  const qc = useQueryClient();
  // The account's appearance wins once per signed-in user.
  const adopted = useRef<string | null>(null);
  useEffect(() => {
    if (!me || adopted.current === me.id) return;
    adopted.current = me.id;
    const fromAccount = normalizeAppearance({ mode: me.theme, palette: me.palette, scale: me.ui_scale });
    const local = storedAppearance();
    if (sameAppearance(fromAccount, DEFAULT_APPEARANCE) && !sameAppearance(local, DEFAULT_APPEARANCE)) {
      // The account never chose: keep what this browser had and save it there.
      void api.PATCH("/me", { body: { theme: local.mode, palette: local.palette, ui_scale: local.scale } }).then((r) => r.data && qc.setQueryData(["me"], r.data));
      return;
    }
    // An external source (the account), synced into local state once per user.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setAppearance(fromAccount);
  }, [me, qc]);

  useEffect(() => {
    const mq = window.matchMedia?.("(prefers-color-scheme: dark)");
    if (!mq) return;
    const on = () => setDark(mq.matches);
    mq.addEventListener("change", on);
    return () => mq.removeEventListener("change", on);
  }, []);

  useEffect(() => {
    applyAppearance(appearance);
  }, [appearance, dark]);

  const update = useCallback(
    (patch: Partial<Appearance>) => {
      setAppearance((a) => ({ ...a, ...patch }));
      if (!me) return;
      const body = { ...(patch.mode && { theme: patch.mode }), ...(patch.palette && { palette: patch.palette }), ...(patch.scale && { ui_scale: patch.scale }) };
      void api.PATCH("/me", { body }).then((r) => r.data && qc.setQueryData(["me"], r.data));
    },
    [me, qc],
  );
  const setChoice = useCallback((mode: ThemeChoice) => update({ mode }), [update]);
  const setPalette = useCallback((palette: Palette) => update({ palette }), [update]);
  const setScale = useCallback((scale: UIScale) => update({ scale }), [update]);
  const value = useMemo(
    () => ({ choice: appearance.mode, resolved: resolveTheme(appearance.mode, dark), palette: appearance.palette, scale: appearance.scale, setChoice, setPalette, setScale }),
    [appearance, dark, setChoice, setPalette, setScale],
  );
  return <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>;
}

export function useTheme(): ThemeState {
  const ctx = useContext(ThemeContext);
  if (!ctx) throw new Error("useTheme outside ThemeProvider");
  return ctx;
}
