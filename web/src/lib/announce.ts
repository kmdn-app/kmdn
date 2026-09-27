import { useEffect, useRef } from "react";

/**
 * Screen reader announcements through one polite live region
 * (docs/specs/02-ux.md#accessibility): collaborators joining or leaving,
 * new suggestions.
 */
let region: HTMLElement | null = null;

function live(): HTMLElement {
  if (region && document.body.contains(region)) return region;
  region = document.createElement("div");
  region.setAttribute("role", "status");
  region.setAttribute("aria-live", "polite");
  region.setAttribute("aria-atomic", "true");
  region.className = "sr-only";
  region.dataset.testid = "announcer";
  document.body.appendChild(region);
  return region;
}

export function announce(message: string) {
  if (typeof document === "undefined" || !message) return;
  const el = live();
  // Clear first so the same message twice is read twice.
  el.textContent = "";
  window.setTimeout(() => (el.textContent = message), 50);
}

/**
 * Announces what joined or left a keyed set between renders (not the
 * first render: that's the page loading, not news).
 */
export function useAnnounceChanges<T>(items: T[] | undefined, key: (x: T) => string, joined: (x: T) => string, left?: (x: T) => string) {
  const prev = useRef<Map<string, T> | null>(null);
  useEffect(() => {
    if (!items) return;
    const now = new Map(items.map((x) => [key(x), x]));
    const before = prev.current;
    prev.current = now;
    if (!before) return;
    const msgs: string[] = [];
    for (const [k, x] of now) if (!before.has(k)) msgs.push(joined(x));
    if (left) for (const [k, x] of before) if (!now.has(k)) msgs.push(left(x));
    if (msgs.length) announce(msgs.join(". "));
    // key/joined/left are expected to be stable in meaning, not identity.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [items]);
}
