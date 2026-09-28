import { Link, useNavigate } from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Building2, Scale, Waypoints } from "lucide-react";
import { Bell, Check, ChevronsUpDown, FilePen, Home, LayoutList, LogOut, Monitor, Moon, Plus, Search, Settings, Shield, Sun, UserRound } from "lucide-react";
import { useState, type ReactNode } from "react";
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
import { FileTree, OP_MARK } from "./file-tree";
import { NewRevisionDialog, RevisionPicker, StateDot, StatePill } from "@/components/revision/revision-ui";
import { ApprovalSummary } from "@/components/revision/review-actions";
import { useNotifications } from "@/lib/notifications";
import { usePresence, useRevisionFiles, useRevisions, useRevisionTree, type RevisionView } from "@/lib/revisions";
import { cn } from "@/lib/utils";
import { usePalette } from "./command-palette";
import { ForgeIcon } from "./forge-icon";
import { currentOrg, orgsInfoQuery, useAssistantStatus, useCurrentOrg, useOrgs } from "@/lib/orgs";

const itemCls =
  "flex h-[1.875rem] w-full items-center gap-2 rounded-[0.4375rem] px-2 text-left text-[0.8125rem] whitespace-nowrap text-sidebar-foreground hover:bg-sidebar-accent [&.active]:bg-sidebar-accent [&.active]:font-medium [&_svg]:size-4 [&_svg]:text-muted-foreground";

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
  const { data: orgs } = useOrgs();
  const { data: orgsInfo } = useQuery(orgsInfoQuery);
  const org = useCurrentOrg();
  const { data: me } = useMe();
  const { data: setup } = useSetupStatus();
  const navigate = useNavigate();
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button className="flex w-full items-center gap-2.5 rounded-lg p-1.5 text-left hover:bg-sidebar-accent" type="button">
          <Logo />
          <span className="min-w-0 flex-1 truncate">
            <b className="block truncate text-[0.8125rem] leading-tight font-semibold">{repo?.display_name ?? (orgs && orgs.length > 1 ? org?.name : setup?.instance_name) ?? "kmdn"}</b>
            <small className="flex items-center gap-1 truncate text-[0.71875rem] leading-tight text-muted-foreground">
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
        {orgs && orgs.length > 1 && (
          <>
            <DropdownMenuLabel className="text-xs text-muted-foreground">{t("orgs.switch")}</DropdownMenuLabel>
            {orgs.map((o) => (
              <DropdownMenuItem key={o.id} onSelect={() => void navigate({ to: "/$org", params: { org: o.slug } })}>
                <Building2 />
                <span className="truncate">{o.name}</span>
                {o.slug === org?.slug && <Check className="ml-auto" />}
              </DropdownMenuItem>
            ))}
            <DropdownMenuSeparator />
          </>
        )}
        {orgsInfo?.mode === "multi" && !orgsInfo.can_create && orgsInfo.signup_url && (
          <>
            <DropdownMenuItem onSelect={() => window.location.assign(orgsInfo.signup_url!)}>
              <Plus />
              {t("orgs.createYours")}
            </DropdownMenuItem>
            <DropdownMenuSeparator />
          </>
        )}
        <DropdownMenuLabel className="text-xs text-muted-foreground">{t("shell.repositories")}</DropdownMenuLabel>
        {(repos ?? []).map((r) => (
          <DropdownMenuItem key={r.id} onSelect={() => void navigate({ to: "/$org/$owner/$repo", params: { org: r.org_slug, owner: r.owner, repo: r.name } })}>
            <ForgeIcon kind={r.forge_kind} />
            <span className="truncate">{r.display_name}</span>
            {r.id === repo?.id && <Check className="ml-auto" />}
          </DropdownMenuItem>
        ))}
        {(repos ?? []).length === 0 && <DropdownMenuItem disabled>{t("home.noRepos")}</DropdownMenuItem>}
        <DropdownMenuSeparator />
        {(me?.is_instance_admin || org?.role === "admin" || org?.role === "owner") && (
          <DropdownMenuItem onSelect={() => void navigate({ to: "/$org/admin", params: { org: currentOrg() }, search: { section: "repositories" } })}>
            <Plus />
            {t("home.connect")}
          </DropdownMenuItem>
        )}
        {repo && atLeast(repo.role, "admin") && (
          <DropdownMenuItem onSelect={() => void navigate({ to: "/$org/$owner/$repo/settings", params: { org: repo.org_slug, owner: repo.owner, repo: repo.name } })}>
            <Settings />
            {t("shell.repoSettings")}
          </DropdownMenuItem>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

function PublishedNav({ repo, currentPath, onNavigate }: { repo: RepoView; currentPath?: string; onNavigate?: () => void }) {
  const { t } = useTranslation();
  const { data: tree, isLoading } = useTree(repo);
  const mine = useRevisions(repo, { mine: true });
  return (
    <>
      {(mine.data?.length ?? 0) > 0 && (
        <Section
          title={t("revision.yours")}
          action={
            <Link to="/$org/$owner/$repo/revisions" params={{ org: repo.org_slug, owner: repo.owner, repo: repo.name }} onClick={onNavigate} className="text-[0.71875rem] font-normal hover:text-foreground">
              {t("revision.viewAll")}
            </Link>
          }
        >
          {mine.data!.slice(0, 5).map((r) => (
            <Link
              key={r.id}
              to="/$org/$owner/$repo/revisions/$number"
              params={{ org: repo.org_slug, owner: repo.owner, repo: repo.name, number: String(r.number) }}
              onClick={onNavigate}
              className={itemCls}
            >
              <StateDot state={r.state} className="mx-[0.3125rem]" />
              <span className="min-w-0 flex-1 truncate">{r.title}</span>
              <span className="text-[0.6875rem] text-muted-foreground">#{r.number}</span>
            </Link>
          ))}
        </Section>
      )}
      <Section title={t("shell.files")}>
        {isLoading || repo.health === "pending" ? (
          <TreeSkeleton />
        ) : tree && tree.items.length > 0 ? (
          <FileTree repo={repo} nodes={tree.items} root={tree.root} current={currentPath} onNavigate={onNavigate} />
        ) : (
          <p className="px-2 text-xs text-muted-foreground">{t("shell.noFiles")}</p>
        )}
      </Section>
    </>
  );
}

function TreeSkeleton() {
  return (
    <div className="grid gap-1.5 px-2 pt-1">
      <Skeleton className="h-4 w-3/4" />
      <Skeleton className="h-4 w-1/2" />
      <Skeleton className="h-4 w-2/3" />
    </div>
  );
}

/** Sidebar while a revision is selected (docs/specs/02-ux.md#contextual-sidebar). */
function RevisionNav({ repo, rev, currentPath, onNavigate }: { repo: RepoView; rev: RevisionView; currentPath?: string; onNavigate?: () => void }) {
  const { t } = useTranslation();
  const files = useRevisionFiles(rev);
  const tree = useRevisionTree(rev);
  const presence = usePresence(rev);
  const online = new Set((presence.data ?? []).map((p) => p.id));
  const [allOpen, setAllOpen] = useState(false);
  return (
    <>
      <Link
        to="/$org/$owner/$repo/revisions/$number"
        params={{ org: repo.org_slug, owner: repo.owner, repo: repo.name, number: String(rev.number) }}
        onClick={onNavigate}
        className="mt-1 block rounded-lg border bg-background p-2.5 hover:bg-accent"
      >
        <StatePill rev={rev} />
        <ApprovalSummary rev={rev} className="mt-1 block" />
        <div className="mt-1.5 text-[0.75rem] text-muted-foreground">{t("revision.overview")} →</div>
      </Link>
      <Section title={t("revision.changed")}>
        {(files.data ?? []).length === 0 && <p className="px-2 text-xs text-muted-foreground">{t("revision.noChanges")}</p>}
        {(files.data ?? []).map((f) => {
          const mark = OP_MARK[f.op];
          const active = f.path === currentPath;
          const body = (
            <>
              <span className={cn("w-3 shrink-0 text-center font-mono text-[0.6875rem] font-semibold", mark?.cls)}>{mark?.letter}</span>
              <span className="min-w-0 flex-1 truncate" title={f.path}>
                {f.path.split("/").pop()}
              </span>
              <span className="shrink-0 font-mono text-[0.65625rem] tabular-nums">
                {f.additions > 0 && <span className="text-success">+{f.additions}</span>}
                {f.deletions > 0 && <span className="ml-1 text-destructive">−{f.deletions}</span>}
              </span>
            </>
          );
          return f.op === "delete" ? (
            <div key={f.id} className={cn(itemCls, "text-muted-foreground line-through decoration-muted-foreground/50")}>
              {body}
            </div>
          ) : (
            <Link
              key={f.id}
              to="/$org/$owner/$repo/$"
              params={{ org: repo.org_slug, owner: repo.owner, repo: repo.name, _splat: f.path }}
              search={{ revision: rev.number }}
              onClick={onNavigate}
              className={cn(itemCls, active && "bg-sidebar-accent font-medium")}
              aria-current={active ? "page" : undefined}
            >
              {body}
            </Link>
          );
        })}
      </Section>
      <Section title={t("revision.people")}>
        {revisionPeople(rev).map((m) => (
          <div key={m.user_id} className={itemCls}>
            <span className="relative">
              <Avatar name={m.name} id={m.user_id} size="sm" />
              {online.has(m.user_id) && <span className="absolute -right-0.5 -bottom-0.5 size-2 rounded-full bg-success ring-2 ring-sidebar" title={t("presence.online")} />}
            </span>
            <span className="min-w-0 flex-1 truncate">{m.name}</span>
            {m.label && <span className="text-[0.6875rem] text-muted-foreground">{t(m.label)}</span>}
          </div>
        ))}
      </Section>
      <Section
        title={t("revision.allFiles")}
        action={
          <button type="button" className="text-[0.71875rem] font-normal hover:text-foreground" onClick={() => setAllOpen((v) => !v)} aria-expanded={allOpen}>
            {allOpen ? t("revision.hide") : t("revision.show")}
          </button>
        }
      >
        {allOpen &&
          (tree.data ? (
            <FileTree repo={repo} nodes={tree.data.items} root={tree.data.root} current={currentPath} revision={rev.number} onNavigate={onNavigate} />
          ) : (
            <TreeSkeleton />
          ))}
      </Section>
    </>
  );
}

export function Sidebar({ repo, revision, currentPath, onNavigate }: { repo?: RepoView; revision?: RevisionView; currentPath?: string; onNavigate?: () => void }) {
  const { t } = useTranslation();
  const { data: me } = useMe();
  const org = useCurrentOrg();
  const canAdmin = !!me?.is_instance_admin || org?.role === "admin" || org?.role === "owner";
  const { choice, setChoice } = useTheme();
  const palette = usePalette();
  const assistant = useAssistantStatus();
  const [newOpen, setNewOpen] = useState(false);
  const qc = useQueryClient();
  const navigate = useNavigate();
  const signOut = async () => {
    await unwrap(api.POST("/auth/logout")).catch(() => undefined);
    qc.setQueryData(meQuery.queryKey, null);
    await navigate({ to: "/signin" });
  };
  return (
    <aside aria-label="Sidebar" className="flex h-full flex-col text-[0.8125rem] text-sidebar-foreground">
      <div className="flex flex-col gap-1.5 px-2.5 pt-2.5 pb-1">
        <RepoSwitcher repo={repo} />
        {repo && <RevisionPicker repo={repo} revision={revision} currentPath={currentPath} onNew={() => setNewOpen(true)} />}
        {repo && atLeast(repo.role, "contributor") && (
          <Button variant="outline" size="sm" className="w-full justify-start bg-background max-md:hidden" onClick={() => setNewOpen(true)}>
            <FilePen />
            {t("shell.newRevision")}
          </Button>
        )}
        {repo && <NewRevisionDialog repo={repo} open={newOpen} onOpenChange={setNewOpen} />}
      </div>
      <nav className="min-h-0 flex-1 overflow-auto px-2.5 pb-3">
        {repo ? (
          <Link to="/$org/$owner/$repo" params={{ org: repo.org_slug, owner: repo.owner, repo: repo.name }} activeOptions={{ exact: true }} onClick={onNavigate} className={itemCls}>
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
            <kbd className="rounded border bg-background px-1 text-[0.65625rem] text-muted-foreground">⌘K</kbd>
          </button>
        )}
        <InboxLink className={itemCls} onNavigate={onNavigate} />
        {repo && !revision && (
          <Link to="/$org/$owner/$repo/revisions" params={{ org: repo.org_slug, owner: repo.owner, repo: repo.name }} onClick={onNavigate} className={itemCls}>
            <LayoutList />
            {t("revision.list")}
          </Link>
        )}
        {repo && !revision && (
          <Link to="/$org/$owner/$repo/graph" params={{ org: repo.org_slug, owner: repo.owner, repo: repo.name }} onClick={onNavigate} className={itemCls}>
            <Waypoints />
            {t("graph.title")}
          </Link>
        )}
        {repo && !revision && assistant.data?.consistency && (
          <Link to="/$org/$owner/$repo/consistency" params={{ org: repo.org_slug, owner: repo.owner, repo: repo.name }} onClick={onNavigate} className={itemCls}>
            <Scale />
            {t("consistency.title")}
          </Link>
        )}
        {repo && (revision ? <RevisionNav repo={repo} rev={revision} currentPath={currentPath} onNavigate={onNavigate} /> : <PublishedNav repo={repo} currentPath={currentPath} onNavigate={onNavigate} />)}
      </nav>
      <div className="flex items-center gap-1 border-t border-sidebar-border px-2.5 py-2">
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <button type="button" className="flex min-w-0 flex-1 items-center gap-2 rounded-[0.4375rem] px-1.5 py-1 text-left hover:bg-sidebar-accent">
              {me && <Avatar name={me.name} id={me.id} size="md" />}
              <span className="min-w-0 truncate">
                <b className="block text-[0.8125rem] leading-tight font-medium">{me?.name}</b>
                <small className="block text-[0.71875rem] text-muted-foreground">{me?.is_instance_admin ? "Admin" : me?.email}</small>
              </span>
            </button>
          </DropdownMenuTrigger>
          <DropdownMenuContent side="top" align="start" className="w-60">
            <DropdownMenuLabel className="truncate font-normal text-muted-foreground">{me?.email}</DropdownMenuLabel>
            <DropdownMenuItem onSelect={() => void navigate({ to: "/settings/profile" })}>
              <UserRound />
              {t("shell.profile")}
            </DropdownMenuItem>
            {canAdmin && (
              <DropdownMenuItem onSelect={() => void navigate({ to: "/$org/admin", params: { org: currentOrg() } })}>
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
        {canAdmin && (
          <Button asChild variant="ghost" size="icon" className="size-8" aria-label={t("shell.admin")}>
            <Link to="/$org/admin" params={{ org: currentOrg() }}>
              <Shield />
            </Link>
          </Button>
        )}
      </div>
    </aside>
  );
}

function InboxLink({ className, onNavigate }: { className: string; onNavigate?: () => void }) {
  const { t } = useTranslation();
  const q = useNotifications();
  const unread = q.data?.unread ?? 0;
  return (
    <Link to="/inbox" onClick={onNavigate} className={className}>
      <Bell />
      <span className="flex-1">{t("shell.inbox")}</span>
      {unread > 0 && <span className="rounded-full bg-primary px-1.5 text-[0.65625rem] leading-4 font-semibold text-primary-foreground tabular-nums">{unread > 99 ? "99+" : unread}</span>}
    </Link>
  );
}

/** Everyone in a revision, once each: editors, then reviewers, then people who took part. */
function revisionPeople(rev: RevisionView): { user_id: string; name: string; label?: string }[] {
  const out: { user_id: string; name: string; label?: string }[] = [];
  const seen = new Set<string>();
  const add = (user_id: string, name: string, label?: string) => {
    if (seen.has(user_id)) return;
    seen.add(user_id);
    out.push({ user_id, name, label });
  };
  for (const m of rev.members) add(m.user_id, m.name, m.role === "owner" ? "revision.owner" : undefined);
  for (const r of rev.reviewers) add(r.user_id, r.name, "revision.reviewer");
  for (const p of rev.participants) add(p.user_id, p.name, "revision.participant");
  return out;
}
