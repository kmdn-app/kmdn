import { useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { FileDiff, History, Link2, MessageSquare, Sparkles, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";

const TABS = [
  { key: "assistant", icon: Sparkles },
  { key: "comments", icon: MessageSquare },
  { key: "changes", icon: FileDiff },
  { key: "history", icon: History },
  { key: "links", icon: Link2 },
] as const;

type TabKey = (typeof TABS)[number]["key"];

/**
 * The tab a page's panel opens on: Comments when the page has something to
 * discuss, otherwise the Assistant. Decided once per page, when `ready`, so a
 * comment arriving later doesn't pull the panel away from the tab someone picked.
 */
export function useInitialPanelTab(page: string, ready: boolean, hasComments: boolean): TabKey {
  const [chosen, setChosen] = useState<{ page: string; tab: TabKey } | null>(null);
  if (ready && chosen?.page !== page) setChosen({ page, tab: hasComments ? "comments" : "assistant" });
  return chosen?.page === page ? chosen.tab : "assistant";
}

/** Docked right panel with Assistant / Comments / Changes / History / Links. */
export type PanelTabs = Partial<Record<(typeof TABS)[number]["key"], ReactNode>> & { initial?: (typeof TABS)[number]["key"] };

export function RightPanel({ onClose, children }: { onClose: () => void; children?: ReactNode | PanelTabs }) {
  const { t } = useTranslation();
  return (
    // Docked on desktops, an overlay on tablets, a bottom sheet on phones.
    <aside
      aria-label="Side panel"
      className="flex w-[360px] shrink-0 flex-col border-l bg-background max-lg:fixed max-lg:inset-y-0 max-lg:right-0 max-lg:z-30 max-lg:w-[min(360px,100%)] max-lg:shadow-xl max-md:inset-x-0 max-md:top-auto max-md:bottom-0 max-md:h-[min(80dvh,680px)] max-md:w-full max-md:rounded-t-2xl max-md:border-t max-md:border-l-0 max-md:pb-[env(safe-area-inset-bottom)]"
    >
      <div className="mx-auto mt-2 h-1 w-10 shrink-0 rounded-full bg-muted-foreground/30 md:hidden" aria-hidden />
      {/* Keyed on the initial tab so pages can switch it (e.g. to Comments). */}
      <Tabs key={panelInitial(children)} defaultValue={panelInitial(children)} className="flex min-h-0 flex-1 flex-col gap-0">
        <div className="flex min-h-[52px] items-center gap-1.5 border-b px-3 py-2">
          <TabsList className="h-8 flex-1">
            {TABS.map(({ key, icon: Icon }) => (
              <TabsTrigger key={key} value={key} title={t(`shell.panel.${key}`)} className="px-1.5 text-xs">
                <Icon className="size-3.5" />
                <span className="sr-only">{t(`shell.panel.${key}`)}</span>
              </TabsTrigger>
            ))}
          </TabsList>
          <Button variant="ghost" size="icon" className="size-7" onClick={onClose} aria-label={t("shell.togglePanel")}>
            <X />
          </Button>
        </div>
        {TABS.map(({ key }) => (
          <TabsContent key={key} value={key} className="min-h-0 flex-1 overflow-auto p-3.5 text-[13.5px]">
            {panelContent(children, key) ?? (
              <>
                <h2 className="mb-1 font-medium">{t(`shell.panel.${key}`)}</h2>
                <p className="text-muted-foreground">{t("shell.panelEmpty")}</p>
              </>
            )}
          </TabsContent>
        ))}
      </Tabs>
    </aside>
  );
}

function isTabs(c: unknown): c is PanelTabs {
  return !!c && typeof c === "object" && !("$$typeof" in (c as object));
}
function panelInitial(c: ReactNode | PanelTabs): string {
  return isTabs(c) && c.initial ? c.initial : "assistant";
}
function panelContent(c: ReactNode | PanelTabs, key: string): ReactNode {
  return isTabs(c) ? (c as unknown as Record<string, ReactNode>)[key] : undefined;
}
