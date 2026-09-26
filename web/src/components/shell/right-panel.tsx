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

/** Docked right panel with Assistant / Comments / Changes / History / Links. */
export function RightPanel({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation();
  return (
    <aside aria-label="Side panel" className="flex w-[360px] shrink-0 flex-col border-l bg-background max-lg:fixed max-lg:inset-y-0 max-lg:right-0 max-lg:z-30 max-lg:w-[min(360px,100%)] max-lg:shadow-xl">
      <Tabs defaultValue="assistant" className="flex min-h-0 flex-1 flex-col gap-0">
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
            <h2 className="mb-1 font-medium">{t(`shell.panel.${key}`)}</h2>
            <p className="text-muted-foreground">{t("shell.panelEmpty")}</p>
          </TabsContent>
        ))}
      </Tabs>
    </aside>
  );
}
