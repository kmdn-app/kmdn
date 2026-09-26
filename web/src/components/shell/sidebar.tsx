import { Link, useNavigate } from "@tanstack/react-router";
import { useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { ChevronsUpDown, FilePen, Home, LogOut, Monitor, Moon, Settings, Shield, Sun } from "lucide-react";
import type { ReactNode } from "react";
import { Logo } from "@/components/logo";
import { Avatar } from "@/components/avatar";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { api, meQuery, unwrap, useMe, useSetupStatus } from "@/lib/api";
import { useTheme } from "@/lib/theme";
import type { ThemeChoice } from "@/theme";

function Item({ to, icon, children, kbd, onNavigate }: { to: string; icon: ReactNode; children: ReactNode; kbd?: string; onNavigate?: () => void }) {
  return (
    <Link
      to={to}
      onClick={onNavigate}
      className="flex h-[30px] items-center gap-2 rounded-[7px] px-2 text-[13px] whitespace-nowrap text-sidebar-foreground hover:bg-sidebar-accent [&.active]:bg-sidebar-accent [&.active]:font-medium [&_svg]:size-4 [&_svg]:text-muted-foreground"
      activeOptions={{ exact: to === "/" }}
    >
      {icon}
      <span className="min-w-0 flex-1 truncate">{children}</span>
      {kbd && <kbd className="rounded border bg-background px-1 text-[10.5px] text-muted-foreground">{kbd}</kbd>}
    </Link>
  );
}

function Section({ title, children }: { title: string; children?: ReactNode }) {
  return (
    <div className="pt-3.5">
      <div className="px-2 pb-1 text-xs font-medium text-muted-foreground">{title}</div>
      {children}
    </div>
  );
}

export function Sidebar({ onNavigate }: { onNavigate?: () => void }) {
  const { t } = useTranslation();
  const { data: me } = useMe();
  const { data: setup } = useSetupStatus();
  const { choice, setChoice } = useTheme();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const signOut = async () => {
    await unwrap(api.POST("/auth/logout")).catch(() => undefined);
    qc.setQueryData(meQuery.queryKey, null);
    await navigate({ to: "/signin" });
  };
  return (
    <aside aria-label="Sidebar" className="flex h-full flex-col text-[13px] text-sidebar-foreground">
      <div className="flex flex-col gap-1.5 px-2.5 pt-2.5 pb-1">
        <button className="flex w-full items-center gap-2.5 rounded-lg p-1.5 text-left hover:bg-sidebar-accent" type="button">
          <Logo />
          <span className="min-w-0 flex-1 truncate">
            <b className="block text-[13px] leading-tight font-semibold">{setup?.instance_name ?? "kmdn"}</b>
            <small className="block text-[11.5px] leading-tight text-muted-foreground">No repository yet</small>
          </span>
          <ChevronsUpDown className="size-3.5 text-muted-foreground" />
        </button>
        <Button variant="outline" size="sm" className="w-full justify-start bg-background" disabled>
          <FilePen />
          {t("shell.newRevision")}
          <kbd className="ml-auto rounded border px-1 text-[10.5px] text-muted-foreground">N</kbd>
        </Button>
      </div>
      <nav className="min-h-0 flex-1 overflow-auto px-2.5 pb-3">
        <Item to="/" icon={<Home />} onNavigate={onNavigate}>
          Home
        </Item>
        <Section title={t("shell.revisions")} />
        <Section title={t("shell.files")} />
      </nav>
      <div className="flex items-center gap-1 border-t border-sidebar-border px-2.5 py-2">
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <button type="button" className="flex min-w-0 flex-1 items-center gap-2 rounded-[7px] px-1.5 py-1 text-left hover:bg-sidebar-accent">
              {me && <Avatar name={me.name} id={me.id} size="md" />}
              <span className="min-w-0 truncate">
                <b className="block text-[13px] leading-tight font-medium">{me?.name}</b>
                <small className="block text-[11.5px] text-muted-foreground">{me?.is_instance_admin ? "Admin" : me?.email}</small>
              </span>
            </button>
          </DropdownMenuTrigger>
          <DropdownMenuContent side="top" align="start" className="w-60">
            <DropdownMenuLabel className="truncate font-normal text-muted-foreground">{me?.email}</DropdownMenuLabel>
            <DropdownMenuItem disabled>
              <Settings />
              {t("shell.profile")}
            </DropdownMenuItem>
            {me?.is_instance_admin && (
              <DropdownMenuItem disabled>
                <Shield />
                {t("shell.admin")}
              </DropdownMenuItem>
            )}
            <DropdownMenuSeparator />
            <DropdownMenuLabel className="text-xs text-muted-foreground">{t("shell.theme")}</DropdownMenuLabel>
            <DropdownMenuRadioGroup value={choice} onValueChange={(v) => setChoice(v as ThemeChoice)}>
              <DropdownMenuRadioItem value="system">
                <Monitor />
                {t("shell.themeSystem")}
              </DropdownMenuRadioItem>
              <DropdownMenuRadioItem value="light">
                <Sun />
                {t("shell.themeLight")}
              </DropdownMenuRadioItem>
              <DropdownMenuRadioItem value="dark">
                <Moon />
                {t("shell.themeDark")}
              </DropdownMenuRadioItem>
            </DropdownMenuRadioGroup>
            <DropdownMenuSeparator />
            <DropdownMenuItem onSelect={() => void signOut()}>
              <LogOut />
              {t("shell.signOut")}
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
    </aside>
  );
}
