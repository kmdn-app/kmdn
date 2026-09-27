import { useEffect, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { cn } from "@/lib/utils";
import { Sidebar } from "./sidebar";
import { useRepos } from "@/lib/repos";
import { lastRepo } from "@/lib/last-repo";
import type { RevisionView } from "@/lib/revisions";
import { CommandPaletteProvider } from "./command-palette";
import type { RepoView } from "@/lib/repos";
import { RightPanel, type PanelTabs } from "./right-panel";
import { AssistantPanel } from "@/components/assistant/assistant-panel";
import { isPhone, onOpenPanel } from "@/lib/media";

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
export function AppShell({
  children,
  repo,
  revision,
  currentPath,
  panel,
}: {
  children: (c: ShellControls) => ReactNode;
  repo?: RepoView;
  /** The selected revision: the sidebar switches to its context. */
  revision?: RevisionView;
  currentPath?: string;
  panel?: ReactNode | PanelTabs;
}) {
  // Phones start with the panel (a bottom sheet) closed, whatever the desktop remembers.
  const [panelOpen, setPanelOpen] = useState(() => !isPhone() && stored(PANEL_KEY, false));
  const [sidebarOpen, setSidebarOpen] = useState(() => stored(SIDEBAR_KEY, true));
  const [mobileNav, setMobileNav] = useState(false);
  const { t } = useTranslation();
  // Pages outside a repository (account, admin) keep the last repository's sidebar.
  const { data: repos } = useRepos();
  const ctxRepo = repo ?? repos?.find((r) => r.id === lastRepo()) ?? repos?.[0];

  const togglePanel = () =>
    setPanelOpen((v) => {
      if (!isPhone()) persist(PANEL_KEY, !v);
      return !v;
    });
  useEffect(() => onOpenPanel(() => setPanelOpen(true)), []);
  const toggleSidebar = () => {
    if (isPhone()) {
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
    <CommandPaletteProvider repo={ctxRepo}>
    <div className="flex h-full min-h-0 bg-background">
      <a
        href="#main"
        onClick={(e) => (e.preventDefault(), document.getElementById("main")?.focus())}
        className="sr-only z-50 rounded-md bg-background px-3 py-2 text-sm font-medium shadow focus:not-sr-only focus:fixed focus:top-2 focus:left-2"
      >
        {t("shell.skipToContent")}
      </a>
      <div
        className={cn(
          "shrink-0 border-r border-sidebar-border bg-sidebar max-md:fixed max-md:inset-y-0 max-md:left-0 max-md:z-40 max-md:w-[min(18.75rem,86%)] max-md:shadow-xl",
          sidebarOpen ? "md:w-[15.5rem]" : "md:hidden",
          !mobileNav && "max-md:hidden",
        )}
      >
        <Sidebar repo={ctxRepo} revision={revision} currentPath={currentPath} onNavigate={() => setMobileNav(false)} />
      </div>
      {mobileNav && <div className="fixed inset-0 z-30 bg-black/40 md:hidden" onClick={() => setMobileNav(false)} aria-hidden />}
      <main id="main" tabIndex={-1} className="flex min-w-0 flex-1 flex-col outline-none">{children({ panelOpen, togglePanel, toggleSidebar })}</main>
      {panelOpen && <div className="fixed inset-0 z-20 bg-black/40 md:hidden" onClick={togglePanel} aria-hidden />}
      {panelOpen && <RightPanel onClose={togglePanel}>{withAssistant(panel, ctxRepo, revision, currentPath)}</RightPanel>}
    </div>
    </CommandPaletteProvider>
  );
}

/** Every repository page gets the Assistant tab unless the page brings its own. */
function withAssistant(panel: ReactNode | PanelTabs | undefined, repo: RepoView | undefined, revision: RevisionView | undefined, path: string | undefined): ReactNode | PanelTabs | undefined {
  if (!repo) return panel;
  const tabs = panel && typeof panel === "object" && !("$$typeof" in (panel as object)) ? (panel as PanelTabs) : panel ? null : {};
  if (!tabs || tabs.assistant) return panel;
  return { ...tabs, assistant: <AssistantPanel key={revision?.id ?? "qa"} repo={repo} path={path} revision={revision} /> };
}
