import { useSyncExternalStore } from "react";

/** Phones: below 768px (docs/specs/02-ux.md#mobile). */
export const PHONE_QUERY = "(max-width: 767px)";

function subscribe(query: string) {
  return (cb: () => void) => {
    const m = window.matchMedia(query);
    m.addEventListener("change", cb);
    return () => m.removeEventListener("change", cb);
  };
}

export function useMedia(query: string): boolean {
  return useSyncExternalStore(subscribe(query), () => window.matchMedia(query).matches, () => false);
}

/** Phones read, comment and review; they don't edit in v1. */
export function useIsPhone(): boolean {
  return useMedia(PHONE_QUERY);
}

export function isPhone(): boolean {
  return typeof window !== "undefined" && window.matchMedia(PHONE_QUERY).matches;
}

const PANEL_EVENT = "kmdn:open-panel";

/** Opens the right panel (a bottom sheet on phones), e.g. on a comment highlight. */
export function openPanel() {
  window.dispatchEvent(new Event(PANEL_EVENT));
}

export function onOpenPanel(cb: () => void): () => void {
  window.addEventListener(PANEL_EVENT, cb);
  return () => window.removeEventListener(PANEL_EVENT, cb);
}
