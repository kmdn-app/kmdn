import { useEffect, useState, type ReactNode } from "react";
import { cn } from "@/lib/utils";
import { Sidebar } from "./sidebar";
import { RightPanel } from "./right-panel";

const PANEL_KEY = "kmdn-panel-open";
const SIDEBAR_KEY = "kmdn-sidebar-open";

function stored(key: string, fallback: boolean) {
  try {
    const v = localStorage.getItem(key);
    return v === null ? fallback : v === "1";
  } catch {
    return fallback;
  }
}
function persist(key: string, v: boolean) {
  try {
    localStorage.setItem(key, v ? "1" : "0");
  } catch {
    /* ignore */
  }
}

export type ShellControls = { panelOpen: boolean; togglePanel: () => void; toggleSidebar: () => void };

/** Docs-first shell: sidebar, main column, docked right panel. See docs/specs/02-ux.md#app-shell. */
export function AppShell({ children }: { children: (c: ShellControls) => ReactNode }) {
  const [panelOpen, setPanelOpen] = useState(() => stored(PANEL_KEY, false));
  const [sidebarOpen, setSidebarOpen] = useState(() => stored(SIDEBAR_KEY, true));
  const [mobileNav, setMobileNav] = useState(false);

  const togglePanel = () =>
    setPanelOpen((v) => {
      persist(PANEL_KEY, !v);
      return !v;
    });
  const toggleSidebar = () => {
    if (window.matchMedia("(max-width: 767px)").matches) {
      setMobileNav((v) => !v);
      return;
    }
    setSidebarOpen((v) => {
      persist(SIDEBAR_KEY, !v);
      return !v;
    });
  };

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (!(e.metaKey || e.ctrlKey)) return;
      if (e.key === ".") {
        e.preventDefault();
        togglePanel();
      } else if (e.key === "\\") {
        e.preventDefault();
        toggleSidebar();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  });

  return (
    <div className="flex h-full min-h-0 bg-background">
      <div
        className={cn(
          "shrink-0 border-r border-sidebar-border bg-sidebar max-md:fixed max-md:inset-y-0 max-md:left-0 max-md:z-40 max-md:w-[min(300px,86%)] max-md:shadow-xl",
          sidebarOpen ? "md:w-[248px]" : "md:hidden",
          !mobileNav && "max-md:hidden",
        )}
      >
        <Sidebar onNavigate={() => setMobileNav(false)} />
      </div>
      {mobileNav && <div className="fixed inset-0 z-30 bg-black/40 md:hidden" onClick={() => setMobileNav(false)} aria-hidden />}
      <main className="flex min-w-0 flex-1 flex-col">{children({ panelOpen, togglePanel, toggleSidebar })}</main>
      {panelOpen && <RightPanel onClose={togglePanel} />}
    </div>
  );
}
