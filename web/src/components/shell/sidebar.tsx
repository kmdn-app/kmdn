import { Link, useNavigate } from "@tanstack/react-router";
import { useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Check, ChevronsUpDown, FilePen, GitBranch, Home, LogOut, Monitor, Moon, Plus, Search, Settings, Shield, Sun, UserRound } from "lucide-react";
import type { ReactNode } from "react";
import { Logo } from "@/components/logo";
import { Avatar } from "@/components/avatar";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
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
import { atLeast, useRepos, useTree, type RepoView } from "@/lib/repos";
import { useTheme } from "@/lib/theme";
import type { ThemeChoice } from "@/theme";
import { FileTree } from "./file-tree";
import { usePalette } from "./command-palette";
import { ForgeIcon } from "./forge-icon";

const itemCls =
  "flex h-[30px] w-full items-center gap-2 rounded-[7px] px-2 text-left text-[13px] whitespace-nowrap text-sidebar-foreground hover:bg-sidebar-accent [&.active]:bg-sidebar-accent [&.active]:font-medium [&_svg]:size-4 [&_svg]:text-muted-foreground";

function Section({ title, action, children }: { title: string; action?: ReactNode; children?: ReactNode }) {
  return (
    <div className="pt-3.5">
      <div className="flex items-center justify-between px-2 pb-1 text-xs font-medium text-muted-foreground">
        {title}
        {action}
      </div>
      {children}
    </div>
  );
}

function RepoSwitcher({ repo }: { repo?: RepoView }) {
  const { t } = useTranslation();
  const { data: repos } = useRepos();
  const { data: me } = useMe();
  const { data: setup } = useSetupStatus();
  const navigate = useNavigate();
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button className="flex w-full items-center gap-2.5 rounded-lg p-1.5 text-left hover:bg-sidebar-accent" type="button">
          <Logo />
          <span className="min-w-0 flex-1 truncate">
            <b className="block truncate text-[13px] leading-tight font-semibold">{repo?.display_name ?? setup?.instance_name ?? "kmdn"}</b>
            <small className="flex items-center gap-1 truncate text-[11.5px] leading-tight text-muted-foreground">
              {repo ? (
                <>
                  <ForgeIcon kind={repo.forge_kind} className="size-3" />
                  {repo.slug}
                  {repo.scope.root && ` · ${repo.scope.root}/`}
                </>
              ) : (
                t("shell.noRepo")
              )}
            </small>
          </span>
          <ChevronsUpDown className="size-3.5 text-muted-foreground" />
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="w-64">
        <DropdownMenuLabel className="text-xs text-muted-foreground">{t("shell.repositories")}</DropdownMenuLabel>
        {(repos ?? []).map((r) => (
          <DropdownMenuItem key={r.id} onSelect={() => void navigate({ to: "/$owner/$repo", params: { owner: r.owner, repo: r.name } })}>
            <ForgeIcon kind={r.forge_kind} />
            <span className="truncate">{r.display_name}</span>
            {r.id === repo?.id && <Check className="ml-auto" />}
          </DropdownMenuItem>
        ))}
        {(repos ?? []).length === 0 && <DropdownMenuItem disabled>{t("home.noRepos")}</DropdownMenuItem>}
        <DropdownMenuSeparator />
        {me?.is_instance_admin && (
          <DropdownMenuItem onSelect={() => void navigate({ to: "/admin", search: { section: "repositories" } })}>
            <Plus />
            {t("home.connect")}
          </DropdownMenuItem>
        )}
        {repo && atLeast(repo.role, "admin") && (
          <DropdownMenuItem onSelect={() => void navigate({ to: "/$owner/$repo/settings", params: { owner: repo.owner, repo: repo.name } })}>
            <Settings />
            {t("shell.repoSettings")}
          </DropdownMenuItem>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

export function Sidebar({ repo, currentPath, onNavigate }: { repo?: RepoView; currentPath?: string; onNavigate?: () => void }) {
  const { t } = useTranslation();
  const { data: me } = useMe();
  const { choice, setChoice } = useTheme();
  const { data: tree, isLoading } = useTree(repo);
  const palette = usePalette();
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
        <RepoSwitcher repo={repo} />
        {repo && (
          <div className="flex h-8 items-center gap-2 rounded-lg border bg-background px-2.5 text-[13px] font-medium shadow-xs" title={t("shell.publishedHint")}>
            <span className="size-1.5 rounded-full bg-success" />
            <span className="truncate">
              {t("shell.published")} <span className="font-normal text-muted-foreground">· {repo.target_branch}</span>
            </span>
            <GitBranch className="ml-auto size-3.5 text-muted-foreground" />
          </div>
        )}
        <Button variant="outline" size="sm" className="w-full justify-start bg-background" disabled title={t("shell.revisionsSoon")}>
          <FilePen />
          {t("shell.newRevision")}
          <kbd className="ml-auto rounded border px-1 text-[10.5px] text-muted-foreground">N</kbd>
        </Button>
      </div>
      <nav className="min-h-0 flex-1 overflow-auto px-2.5 pb-3">
        {repo ? (
          <Link to="/$owner/$repo" params={{ owner: repo.owner, repo: repo.name }} activeOptions={{ exact: true }} onClick={onNavigate} className={itemCls}>
            <Home />
            {t("shell.home")}
          </Link>
        ) : (
          <Link to="/" activeOptions={{ exact: true }} onClick={onNavigate} className={itemCls}>
            <Home />
            {t("shell.home")}
          </Link>
        )}
        {repo && (
          <button type="button" className={itemCls} onClick={() => palette.open()}>
            <Search />
            <span className="flex-1">{t("shell.search")}</span>
            <kbd className="rounded border bg-background px-1 text-[10.5px] text-muted-foreground">⌘K</kbd>
          </button>
        )}
        {repo && (
          <Section title={t("shell.files")}>
            {isLoading || repo.health === "pending" ? (
              <div className="grid gap-1.5 px-2 pt-1">
                <Skeleton className="h-4 w-3/4" />
                <Skeleton className="h-4 w-1/2" />
                <Skeleton className="h-4 w-2/3" />
              </div>
            ) : tree && tree.items.length > 0 ? (
              <FileTree repo={repo} nodes={tree.items} root={tree.root} current={currentPath} onNavigate={onNavigate} />
            ) : (
              <p className="px-2 text-xs text-muted-foreground">{t("shell.noFiles")}</p>
            )}
          </Section>
        )}
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
            <DropdownMenuItem onSelect={() => void navigate({ to: "/settings/profile" })}>
              <UserRound />
              {t("shell.profile")}
            </DropdownMenuItem>
            {me?.is_instance_admin && (
              <DropdownMenuItem onSelect={() => void navigate({ to: "/admin" })}>
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
        {me?.is_instance_admin && (
          <Button asChild variant="ghost" size="icon" className="size-8" aria-label={t("shell.admin")}>
            <Link to="/admin">
              <Shield />
            </Link>
          </Button>
        )}
      </div>
    </aside>
  );
}
