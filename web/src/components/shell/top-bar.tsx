import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Menu, PanelRight } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import type { ShellControls } from "./app-shell";

export function TopBar({ controls, title, actions }: { controls: ShellControls; title: ReactNode; actions?: ReactNode }) {
  const { t } = useTranslation();
  return (
    // A container: actions collapse by the bar's own width (the side panels take room).
    <div className="@container flex min-h-[3.25rem] shrink-0 items-center gap-2 border-b px-4 py-2 max-sm:px-2.5">
      <Button variant="ghost" size="icon" className="size-8" onClick={controls.toggleSidebar} aria-label={t("shell.toggleSidebar")}>
        <Menu />
      </Button>
      <nav aria-label="Breadcrumb" className="min-w-[min(8rem,30%)] truncate text-[0.84375rem] font-medium">
        {title}
      </nav>
      <span className="min-w-2 flex-1" />
      {actions}
      <Tooltip>
        <TooltipTrigger asChild>
          <Button variant="ghost" size="icon" className="size-8" onClick={controls.togglePanel} aria-label={t("shell.togglePanel")} aria-pressed={controls.panelOpen}>
            <PanelRight />
          </Button>
        </TooltipTrigger>
        <TooltipContent>{t("shell.togglePanel")} · ⌘.</TooltipContent>
      </Tooltip>
    </div>
  );
}
