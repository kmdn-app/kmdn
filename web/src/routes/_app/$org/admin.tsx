import { useRef, useState } from "react";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { Activity, Building2, CheckCircle2, GitBranch, Grid2x2, KeyRound, Loader2, Mail, MoreHorizontal, Plus, ScrollText, Server, Shield, Sparkles, Trash2, TriangleAlert, UserCheck, UserPlus, UserX, Users } from "lucide-react";
import type { components, paths } from "@kmdn/api-client";
import { AppShell } from "@/components/shell/app-shell";
import { TopBar } from "@/components/shell/top-bar";
import { CopyButton } from "@/components/copy-button";
import { Card, Panel, SettingsLayout } from "@/components/settings-layout";
import { Avatar } from "@/components/avatar";
import { Time } from "@/components/time";
import { ForgeIcon } from "@/components/shell/forge-icon";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Label } from "@/components/ui/label";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { api, errorMessage, unwrap, useMe } from "@/lib/api";
import { currentOrg, orgsInfoQuery, useCurrentOrg } from "@/lib/orgs";
import { OrganizationPanel, OrgsPanel } from "@/components/admin/org";
import { useRepos } from "@/lib/repos";
import { cn } from "@/lib/utils";
import { SmtpForm } from "@/components/smtp-form";
import { AgentKeysPanel } from "@/components/admin/agent-keys";
import { AuditLogPanel } from "@/components/admin/audit-log";
import { SystemPanel } from "@/components/admin/system";

type AdminUser = components["schemas"]["AdminUser"];
type ForgeHost = components["schemas"]["ForgeHost"];

const SECTIONS = ["members", "users", "groups", "repositories", "organization", "agent-keys", "audit", "orgs", "email", "ai", "system"] as const;
type Section = (typeof SECTIONS)[number];

export const Route = createFileRoute("/_app/$org/admin")({
  validateSearch: (s: Record<string, unknown>): { section?: Section; forge_error?: string } => ({
    ...(SECTIONS.includes(s.section as Section) ? { section: s.section as Section } : {}),
    ...(typeof s.forge_error === "string" ? { forge_error: s.forge_error } : {}),
  }),
  component: Admin,
});

/**
 * The console: the org's sections for its admins, and the instance's for
 * instance admins (docs/specs/16-organizations.md#roles). In single mode an
 * instance admin sees one console, as before orgs.
 */
function Admin() {
  const { t } = useTranslation();
  const { data: me } = useMe();
  const org = useCurrentOrg();
  const info = useQuery(orgsInfoQuery);
  const instance = !!me?.is_instance_admin;
  const orgAdmin = instance || org?.role === "admin" || org?.role === "owner";
  const multi = info.data?.mode === "multi";
  const sections = [
    ...(multi || !instance ? [{ key: "members", label: t("orgs.members"), icon: Users }] : []),
    ...(instance ? [{ key: "users", label: t("admin.users"), icon: UserCheck }] : []),
    { key: "groups", label: t("admin.groups"), icon: Grid2x2 },
    { key: "repositories", label: t("admin.repositories"), icon: Server },
    { key: "organization", label: t("orgs.organization"), icon: Building2 },
    { key: "agent-keys", label: t("admin.agentKeys"), icon: KeyRound },
    { key: "audit", label: t("admin.audit"), icon: ScrollText },
    ...(instance && multi ? [{ key: "orgs", label: t("orgs.all"), icon: Building2 }] : []),
    ...(instance
      ? [
          { key: "email", label: t("admin.email"), icon: Mail },
          { key: "ai", label: t("ai.title"), icon: Sparkles },
          { key: "system", label: t("admin.system"), icon: Activity },
        ]
      : []),
  ];
  const search = Route.useSearch();
  const section = search.section && sections.some((s) => s.key === search.section) ? search.section : (sections[0]!.key as Section);
  const { forge_error } = search;
  return (
    <AppShell>
      {(controls) => (
        <>
          <TopBar controls={controls} title={t("admin.title")} />
          <div className="min-h-0 flex-1 overflow-auto">
            {!orgAdmin ? (
              <p className="p-8 text-muted-foreground">{t("admin.onlyOrg")}</p>
            ) : (
              <SettingsLayout
                title={instance && !multi ? t("admin.instance") : (org?.name ?? t("admin.org"))}
                active={section}
                sections={sections}
                linkProps={(key) => ({ to: "/$org/admin", params: { org: currentOrg() }, search: { section: key as Section } })}
              >
                {forge_error && (
                  <p role="alert" className="mb-4 rounded-lg border border-destructive/30 bg-destructive/5 p-3 text-[0.84375rem]">
                    {t("admin.forgeError", { code: forge_error })}
                  </p>
                )}
                {section === "members" && <MembersPanel />}
                {section === "users" && <UsersPanel />}
                {section === "organization" && <OrganizationPanel />}
                {section === "orgs" && <OrgsPanel />}
                {section === "groups" && <GroupsPanel />}
                {section === "repositories" && <ReposPanel />}
                {section === "email" && <EmailPanel />}
                {section === "ai" && <AIPanel />}
                {section === "agent-keys" && <AgentKeysPanel />}
                {section === "audit" && <AuditLogPanel />}
                {section === "system" && <SystemPanel />}
              </SettingsLayout>
            )}
          </div>
        </>
      )}
    </AppShell>
  );
}

type OrgMember = components["schemas"]["OrgMember"];

/** Org console → Members: roles in the org, deactivation, removal. */
function MembersPanel() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const org = useCurrentOrg();
  const { data: me } = useMe();
  const [inviteOpen, setInviteOpen] = useState(false);
  const members = useQuery({ queryKey: ["org-members", currentOrg()], queryFn: async () => (await unwrap(api.GET("/orgs/{org}/members", { params: { path: { org: currentOrg() } } }))).items });
  const refresh = () => void qc.invalidateQueries({ queryKey: ["org-members"] });
  const update = useMutation({
    mutationFn: (v: { id: string; body: { role?: "member" | "admin" | "owner"; status?: "active" | "deactivated" } }) =>
      unwrap(api.PATCH("/orgs/{org}/members/{user}", { params: { path: { org: currentOrg(), user: v.id } }, body: v.body })),
    onSuccess: refresh,
    onError: (e) => toast.error(errorMessage(e, t("errors.generic"))),
  });
  const remove = useMutation({
    mutationFn: (id: string) => unwrap(api.DELETE("/orgs/{org}/members/{user}", { params: { path: { org: currentOrg(), user: id } } })),
    onSuccess: refresh,
    onError: (e) => toast.error(errorMessage(e, t("errors.generic"))),
  });
  const owner = org?.role === "owner" || !!me?.is_instance_admin;
  return (
    <Panel
      title={t("orgs.members")}
      desc={t("orgs.membersDesc")}
      actions={
        <Button size="sm" onClick={() => setInviteOpen(true)}>
          <UserPlus />
          {t("admin.invite")}
        </Button>
      }
    >
      <div className="overflow-x-auto rounded-xl border">
        <table className="w-full text-[0.84375rem]">
          <tbody>
            {(members.data ?? []).map((m: OrgMember) => (
              <tr key={m.id} className="border-b last:border-0">
                <td className="px-3 py-2.5">
                  <div className="flex items-center gap-2.5">
                    <Avatar name={m.name} id={m.id} size="md" />
                    <div className="min-w-0">
                      <div className="font-medium whitespace-nowrap">{m.name}</div>
                      <div className="truncate text-xs text-muted-foreground">{m.email}</div>
                    </div>
                  </div>
                </td>
                <td className="px-3 py-2.5">
                  <Badge variant={m.role === "member" ? "outline" : "secondary"}>{t(`orgs.role_${m.role}`)}</Badge>
                  {m.status === "deactivated" && (
                    <Badge variant="outline" className="ml-1.5">
                      {t("admin.status_deactivated")}
                    </Badge>
                  )}
                </td>
                <td className="w-10 px-2 py-2.5 text-right">
                  <DropdownMenu>
                    <DropdownMenuTrigger asChild>
                      <Button variant="ghost" size="icon" className="size-8" aria-label={t("admin.userActions", { name: m.name })}>
                        <MoreHorizontal />
                      </Button>
                    </DropdownMenuTrigger>
                    <DropdownMenuContent align="end">
                      {(["member", "admin", ...(owner ? (["owner"] as const) : [])] as const)
                        .filter((r) => r !== m.role && (owner || m.role !== "owner"))
                        .map((r) => (
                          <DropdownMenuItem key={r} onSelect={() => update.mutate({ id: m.id, body: { role: r } })}>
                            <Shield />
                            {t(r === "member" ? "orgs.makeMember" : r === "admin" ? "orgs.makeAdmin" : "orgs.makeOwner")}
                          </DropdownMenuItem>
                        ))}
                      <DropdownMenuItem onSelect={() => update.mutate({ id: m.id, body: { status: m.status === "active" ? "deactivated" : "active" } })}>
                        {m.status === "active" ? <UserX /> : <UserCheck />}
                        {m.status === "active" ? t("orgs.deactivate") : t("orgs.reactivate")}
                      </DropdownMenuItem>
                      <DropdownMenuItem variant="destructive" onSelect={() => remove.mutate(m.id)}>
                        <Trash2 />
                        {t("orgs.remove")}
                      </DropdownMenuItem>
                    </DropdownMenuContent>
                  </DropdownMenu>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <InviteDialog open={inviteOpen} onOpenChange={setInviteOpen} />
    </Panel>
  );
}

function UsersPanel() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [q, setQ] = useState("");
  const [inviteOpen, setInviteOpen] = useState(false);
  const users = useQuery({ queryKey: ["admin-users", q], queryFn: async () => (await unwrap(api.GET("/admin/users", { params: { query: q ? { q } : {} } }))).items });
  const update = useMutation({
    mutationFn: (v: { id: string; body: { is_instance_admin?: boolean; status?: "active" | "deactivated" } }) => unwrap(api.PATCH("/admin/users/{id}", { params: { path: { id: v.id } }, body: v.body })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["admin-users"] }),
    onError: (e) => toast.error(errorMessage(e, t("errors.generic"))),
  });
  return (
    <Panel
      title={t("admin.users")}
      desc={t("admin.usersDesc")}
      actions={
        <Button size="sm" onClick={() => setInviteOpen(true)}>
          <UserPlus />
          {t("admin.invite")}
        </Button>
      }
    >
      <Input placeholder={t("admin.searchUsers")} value={q} onChange={(e) => setQ(e.target.value)} aria-label={t("admin.searchUsers")} />
      <div className="overflow-x-auto rounded-xl border">
        <table className="w-full text-[0.84375rem]">
          <thead className="text-left text-[0.8125rem] text-muted-foreground">
            <tr className="border-b">
              <th className="px-3 py-2.5 font-medium">{t("admin.user")}</th>
              <th className="px-3 py-2.5 font-medium max-md:hidden">{t("admin.repositories")}</th>
              <th className="px-3 py-2.5 font-medium">{t("admin.status")}</th>
              <th className="px-3 py-2.5 font-medium max-md:hidden">{t("admin.lastActive")}</th>
              <th className="px-3 py-2.5" />
            </tr>
          </thead>
          <tbody>
            {(users.data ?? []).map((u: AdminUser) => (
              <tr key={u.id} className="border-b last:border-0">
                <td className="px-3 py-2.5">
                  <div className="flex items-center gap-2.5">
                    <Avatar name={u.name} id={u.id} size="md" />
                    <div className="min-w-0">
                      <div className="flex items-center gap-1.5 font-medium whitespace-nowrap">
                        {u.name}
                        {u.is_instance_admin && <Badge>{t("admin.instanceAdmin")}</Badge>}
                      </div>
                      <div className="truncate text-xs text-muted-foreground">{u.email}</div>
                    </div>
                  </div>
                </td>
                <td className="px-3 py-2.5 tabular-nums max-md:hidden">{u.repos}</td>
                <td className="px-3 py-2.5">
                  <Badge variant={u.status === "active" ? "secondary" : "outline"}>{t(`admin.status_${u.status}`)}</Badge>
                </td>
                <td className="px-3 py-2.5 text-muted-foreground max-md:hidden">{u.last_active_at ? <Time iso={u.last_active_at} /> : "—"}</td>
                <td className="w-10 px-2 py-2.5 text-right">
                  <DropdownMenu>
                    <DropdownMenuTrigger asChild>
                      <Button variant="ghost" size="icon" className="size-8" aria-label={t("admin.userActions", { name: u.name })}>
                        <MoreHorizontal />
                      </Button>
                    </DropdownMenuTrigger>
                    <DropdownMenuContent align="end">
                      <DropdownMenuItem onSelect={() => update.mutate({ id: u.id, body: { is_instance_admin: !u.is_instance_admin } })}>
                        <Shield />
                        {u.is_instance_admin ? t("admin.removeAdmin") : t("admin.makeAdmin")}
                      </DropdownMenuItem>
                      <DropdownMenuItem
                        variant={u.status === "active" ? "destructive" : "default"}
                        onSelect={() => update.mutate({ id: u.id, body: { status: u.status === "active" ? "deactivated" : "active" } })}
                      >
                        {u.status === "active" ? <UserX /> : <UserCheck />}
                        {u.status === "active" ? t("admin.deactivate") : t("admin.reactivate")}
                      </DropdownMenuItem>
                    </DropdownMenuContent>
                  </DropdownMenu>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <InviteDialog open={inviteOpen} onOpenChange={setInviteOpen} />
    </Panel>
  );
}

function InviteDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (v: boolean) => void }) {
  const { t } = useTranslation();
  const { data: repos } = useRepos();
  const [email, setEmail] = useState("");
  const [repoID, setRepoID] = useState<string>("none");
  const [role, setRole] = useState<"viewer" | "contributor" | "maintainer" | "admin">("contributor");
  const invite = useMutation({
    mutationFn: () => unwrap(api.POST("/orgs/{org}/admin/invites", { params: { path: { org: currentOrg() } }, body: { email, ...(repoID !== "none" ? { repo_id: repoID, role } : {}) } })),
    onSuccess: (r) => {
      toast.success(r.status === "invited" ? t("settings.invited", { email: r.email }) : t("settings.added"));
      setEmail("");
      onOpenChange(false);
    },
  });
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("admin.invite")}</DialogTitle>
          <DialogDescription>{t("admin.inviteDesc")}</DialogDescription>
        </DialogHeader>
        <div className="grid gap-4">
          <div className="grid gap-1.5">
            <Label htmlFor="inv-email">{t("admin.emailLabel")}</Label>
            <Input id="inv-email" type="email" value={email} onChange={(e) => setEmail(e.target.value)} />
          </div>
          <div className="grid grid-cols-2 gap-3">
            <div className="grid gap-1.5">
              <Label>{t("admin.repository")}</Label>
              <Select value={repoID} onValueChange={setRepoID}>
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="none">{t("admin.noRepo")}</SelectItem>
                  {(repos ?? []).map((r) => (
                    <SelectItem key={r.id} value={r.id}>
                      {r.display_name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="grid gap-1.5">
              <Label>{t("settings.role")}</Label>
              <Select value={role} onValueChange={(v) => setRole(v as typeof role)} disabled={repoID === "none"}>
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {(["viewer", "contributor", "maintainer", "admin"] as const).map((r) => (
                    <SelectItem key={r} value={r}>
                      {t(`roles.${r}`)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </div>
          {invite.error && <p className="text-[0.8125rem] text-destructive">{errorMessage(invite.error, t("errors.generic"))}</p>}
        </div>
        <DialogFooter>
          <Button onClick={() => invite.mutate()} disabled={!email || invite.isPending}>
            {invite.isPending && <Loader2 className="animate-spin" />}
            {t("admin.sendInvite")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function GroupsPanel() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [name, setName] = useState("");
  const [openGroup, setOpenGroup] = useState<string | null>(null);
  const groups = useQuery({ queryKey: ["admin-groups"], queryFn: async () => (await unwrap(api.GET("/orgs/{org}/admin/groups", { params: { path: { org: currentOrg() } } }))).items });
  const create = useMutation({
    mutationFn: () => unwrap(api.POST("/orgs/{org}/admin/groups", { params: { path: { org: currentOrg() } }, body: { name } })),
    onSuccess: () => {
      setName("");
      void qc.invalidateQueries({ queryKey: ["admin-groups"] });
    },
    onError: (e) => toast.error(errorMessage(e, t("errors.generic"))),
  });
  const del = useMutation({
    mutationFn: (id: string) => unwrap(api.DELETE("/orgs/{org}/admin/groups/{id}", { params: { path: { org: currentOrg(), id } } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["admin-groups"] }),
  });
  return (
    <Panel title={t("admin.groups")} desc={t("admin.groupsDesc")}>
      <form
        className="flex gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          create.mutate();
        }}
      >
        <Input placeholder={t("admin.groupName")} value={name} onChange={(e) => setName(e.target.value)} aria-label={t("admin.groupName")} />
        <Button type="submit" disabled={!name.trim()}>
          <Plus />
          {t("admin.newGroup")}
        </Button>
      </form>
      <div className="divide-y rounded-xl border">
        {(groups.data ?? []).map((g) => (
          <div key={g.id}>
            <div className="flex items-center gap-3 px-4 py-3">
              <span className="grid size-8 place-items-center rounded-full bg-muted">
                <Users className="size-4 text-muted-foreground" />
              </span>
              <button type="button" className="min-w-0 flex-1 text-left" onClick={() => setOpenGroup(openGroup === g.id ? null : g.id)} aria-expanded={openGroup === g.id}>
                <div className="font-medium">{g.name}</div>
                <div className="text-xs text-muted-foreground">{t("settings.groupMembers", { count: g.members })}</div>
              </button>
              <Button variant="ghost" size="icon" className="size-8" aria-label={t("admin.deleteGroup", { name: g.name })} onClick={() => del.mutate(g.id)}>
                <Trash2 />
              </Button>
            </div>
            {openGroup === g.id && <GroupMembers groupID={g.id} />}
          </div>
        ))}
        {groups.data?.length === 0 && <p className="px-4 py-6 text-center text-muted-foreground">{t("admin.noGroups")}</p>}
      </div>
    </Panel>
  );
}

function GroupMembers({ groupID }: { groupID: string }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [q, setQ] = useState("");
  const key = ["group-members", groupID];
  const members = useQuery({ queryKey: key, queryFn: async () => (await unwrap(api.GET("/orgs/{org}/admin/groups/{id}/members", { params: { path: { org: currentOrg(), id: groupID } } }))).items });
  const found = useQuery({ queryKey: ["users", q], queryFn: async () => (await unwrap(api.GET("/orgs/{org}/users", { params: { path: { org: currentOrg() }, query: { q } } }))).items, enabled: q.trim().length > 1 });
  const add = useMutation({
    mutationFn: (user: string) => unwrap(api.PUT("/orgs/{org}/admin/groups/{id}/members/{user}", { params: { path: { org: currentOrg(), id: groupID, user } } })),
    onSuccess: (d) => {
      qc.setQueryData(key, d.items);
      setQ("");
      void qc.invalidateQueries({ queryKey: ["admin-groups"] });
    },
  });
  const remove = useMutation({
    mutationFn: (user: string) => unwrap(api.DELETE("/orgs/{org}/admin/groups/{id}/members/{user}", { params: { path: { org: currentOrg(), id: groupID, user } } })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: key });
      void qc.invalidateQueries({ queryKey: ["admin-groups"] });
    },
  });
  return (
    <div className="grid gap-2 border-t bg-muted/30 px-4 py-3">
      {(members.data ?? []).map((u) => (
        <div key={u.id} className="flex items-center gap-2.5 text-[0.84375rem]">
          <Avatar name={u.name} id={u.id} size="sm" />
          <span className="font-medium">{u.name}</span>
          <span className="truncate text-muted-foreground">{u.email}</span>
          <Button variant="ghost" size="sm" className="ml-auto h-7" onClick={() => remove.mutate(u.id)}>
            {t("admin.removeFromGroup")}
          </Button>
        </div>
      ))}
      <div className="relative">
        <Input className="h-8" placeholder={t("admin.addToGroup")} value={q} onChange={(e) => setQ(e.target.value)} aria-label={t("admin.addToGroup")} />
        {(found.data?.length ?? 0) > 0 && (
          <div className="absolute inset-x-0 top-full z-10 mt-1 overflow-hidden rounded-lg border bg-popover shadow-md">
            {found.data!.map((u) => (
              <button key={u.id} type="button" className="flex w-full items-center gap-2.5 px-3 py-2 text-left text-[0.84375rem] hover:bg-accent" onClick={() => add.mutate(u.id)}>
                <Avatar name={u.name} id={u.id} size="sm" />
                {u.name} <span className="text-muted-foreground">{u.email}</span>
              </button>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}

function ReposPanel() {
  const { t } = useTranslation();
  const { data: repos } = useRepos();
  // The forges this org can use: the instance's shared ones and its own.
  const hosts = useQuery({ queryKey: ["forges", currentOrg()], queryFn: async () => (await unwrap(api.GET("/orgs/{org}/admin/forges", { params: { path: { org: currentOrg() } } }))).items });
  const [connectHost, setConnectHost] = useState<ForgeHost | null>(null);
  const [addGitLab, setAddGitLab] = useState(false);
  return (
    <Panel title={t("admin.repositories")} desc={t("admin.reposDesc")}>
      <div className="grid gap-3 sm:grid-cols-2">
        {(hosts.data ?? []).map((h) => (
          <Card key={h.id}>
            <div className="flex items-center gap-3">
              <ForgeIcon kind={h.kind} className="size-5" />
              <div className="min-w-0 flex-1">
                <div className="font-medium">{h.display_name}</div>
                <div className="truncate text-xs text-muted-foreground">
                  {h.kind === "git" ? t("admin.plainGit") : h.base_url} · {t("admin.reposCount", { count: h.repos })}
                </div>
              </div>
              <Button size="sm" variant="outline" onClick={() => setConnectHost(h)}>
                <Plus />
                {t("admin.connect")}
              </Button>
            </div>
          </Card>
        ))}
      </div>
      <div className="flex flex-wrap gap-2">
        <GitHubAppButton />
        <Button variant="outline" onClick={() => setAddGitLab(true)}>
          <ForgeIcon kind="gitlab" />
          {t("admin.addGitLab")}
        </Button>
      </div>
      <div className="overflow-x-auto rounded-xl border">
        <table className="w-full text-[0.84375rem]">
          <thead className="text-left text-[0.8125rem] text-muted-foreground">
            <tr className="border-b">
              <th className="px-3 py-2.5 font-medium">{t("admin.repository")}</th>
              <th className="px-3 py-2.5 font-medium max-md:hidden">{t("settings.contentRoot")}</th>
              <th className="px-3 py-2.5 font-medium max-md:hidden">{t("admin.publishing")}</th>
              <th className="px-3 py-2.5 font-medium">{t("admin.health")}</th>
            </tr>
          </thead>
          <tbody>
            {(repos ?? []).map((r) => (
              <tr key={r.id} className="border-b last:border-0">
                <td className="px-3 py-2.5">
                  <Link to="/$org/$owner/$repo/settings" params={{ org: r.org_slug, owner: r.owner, repo: r.name }} className="flex items-center gap-2 font-medium hover:underline">
                    <ForgeIcon kind={r.forge_kind} className="size-3.5" />
                    {r.display_name}
                  </Link>
                </td>
                <td className="px-3 py-2.5 font-mono text-[0.78125rem] max-md:hidden">{r.scope.root ? `${r.scope.root}/` : "/"}</td>
                <td className="px-3 py-2.5 max-md:hidden">{r.protection.protected ? t("admin.viaPR") : t("admin.direct")}</td>
                <td className="px-3 py-2.5">
                  <Badge variant={r.health === "ok" ? "secondary" : "outline"} className={cn(r.health !== "ok" && r.health !== "pending" && "border-destructive/40 text-destructive")}>
                    {t(`admin.health_${r.health}`)}
                  </Badge>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {connectHost && <ConnectDialog host={connectHost} onClose={() => setConnectHost(null)} />}
      <AddGitLabDialog open={addGitLab} onOpenChange={setAddGitLab} />
    </Panel>
  );
}

/** Starts the GitHub App manifest flow: POSTs a form to GitHub. */
function GitHubAppButton() {
  const { t } = useTranslation();
  const { data: me } = useMe();
  const formRef = useRef<HTMLFormElement>(null);
  const [target, setTarget] = useState<{ action: string; manifest: string } | null>(null);
  const [open, setOpen] = useState(false);
  const [base, setBase] = useState("");
  const [org, setOrg] = useState("");
  const start = useMutation({
    // Instance admins add Apps every org shares; org admins add their org's own.
    mutationFn: () => {
      const body = { base_url: base || undefined, organization: org || undefined };
      return unwrap(me?.is_instance_admin ? api.POST("/admin/forges/github/manifest", { body }) : api.POST("/orgs/{org}/admin/forges/github/manifest", { params: { path: { org: currentOrg() } }, body }));
    },
    onSuccess: (r) => {
      setTarget({ action: r.action_url, manifest: r.manifest });
      setTimeout(() => formRef.current?.submit(), 0);
    },
  });
  return (
    <>
      <Button variant="outline" onClick={() => setOpen(true)}>
        <ForgeIcon kind="github" />
        {t("admin.createGitHubApp")}
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("admin.createGitHubApp")}</DialogTitle>
            <DialogDescription>{t("admin.githubAppDesc")}</DialogDescription>
          </DialogHeader>
          <div className="grid gap-3">
            <div className="grid gap-1.5">
              <Label htmlFor="gh-org">{t("admin.githubOrg")}</Label>
              <Input id="gh-org" placeholder="northwind" value={org} onChange={(e) => setOrg(e.target.value)} />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="gh-base">{t("admin.githubBase")}</Label>
              <Input id="gh-base" placeholder="https://github.com" value={base} onChange={(e) => setBase(e.target.value)} />
            </div>
            {start.error && <p className="text-[0.8125rem] text-destructive">{errorMessage(start.error, t("errors.generic"))}</p>}
          </div>
          <DialogFooter>
            <Button onClick={() => start.mutate()} disabled={start.isPending}>
              {start.isPending && <Loader2 className="animate-spin" />}
              {t("admin.continueToGitHub")}
            </Button>
          </DialogFooter>
          {target && (
            <form ref={formRef} method="post" action={target.action} className="hidden">
              <input type="hidden" name="manifest" value={target.manifest} />
            </form>
          )}
        </DialogContent>
      </Dialog>
    </>
  );
}

function AddGitLabDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (v: boolean) => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [form, setForm] = useState({ base_url: "", client_id: "", client_secret: "" });
  const { data: me } = useMe();
  const callback = useQuery({
    queryKey: ["forges", "gitlab-callback"],
    queryFn: async () => (await unwrap(api.GET("/orgs/{org}/admin/forges", { params: { path: { org: currentOrg() } } }))).gitlab_callback_url,
    enabled: open,
  });
  const add = useMutation({
    mutationFn: () => {
      const body = { kind: "gitlab" as const, ...form };
      return unwrap(me?.is_instance_admin ? api.POST("/admin/forges", { body }) : api.POST("/orgs/{org}/admin/forges", { params: { path: { org: currentOrg() } }, body }));
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["forges"] });
      onOpenChange(false);
    },
  });
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("admin.addGitLab")}</DialogTitle>
          <DialogDescription>{t("admin.gitlabDesc")}</DialogDescription>
        </DialogHeader>
        <div className="grid gap-3">
          <div className="grid gap-1.5">
            <Label htmlFor="gl-url">GitLab URL</Label>
            <Input id="gl-url" value={form.base_url} onChange={(e) => setForm({ ...form, base_url: e.target.value })} placeholder="https://gitlab.com · https://gitlab.example.com" />
          </div>
          <div className="grid gap-1.5 border-t pt-3">
            <div className="text-[0.84375rem] font-medium">{t("admin.gitlabOAuth")}</div>
            <p className="text-[0.78125rem] text-muted-foreground">{t("admin.gitlabOAuthHint")}</p>
          </div>
          {callback.data && (
            <div className="grid gap-1.5">
              <Label htmlFor="gl-callback">{t("admin.gitlabCallback")}</Label>
              <div className="flex gap-2">
                <Input id="gl-callback" readOnly value={callback.data} className="font-mono text-[0.78125rem]" onFocus={(e) => e.currentTarget.select()} />
                <CopyButton text={callback.data} label={t("admin.copy")} />
              </div>
            </div>
          )}
          <div className="grid gap-1.5">
            <Label htmlFor="gl-id">{t("admin.oauthClientId")}</Label>
            <Input id="gl-id" value={form.client_id} onChange={(e) => setForm({ ...form, client_id: e.target.value })} />
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="gl-secret">{t("admin.oauthClientSecret")}</Label>
            <Input id="gl-secret" type="password" value={form.client_secret} onChange={(e) => setForm({ ...form, client_secret: e.target.value })} />
          </div>
          {add.error && <p className="text-[0.8125rem] text-destructive">{errorMessage(add.error, t("errors.generic"))}</p>}
        </div>
        <DialogFooter>
          <Button onClick={() => add.mutate()} disabled={add.isPending}>
            {t("admin.add")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function ConnectDialog({ host, onClose }: { host: ForgeHost; onClose: () => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [form, setForm] = useState({ owner: "", name: "", clone_url: "", token: "", target_branch: "", content_root: "" });
  const [install, setInstall] = useState("");
  const installs = useQuery({
    queryKey: ["installs", host.id],
    queryFn: () => unwrap(api.GET("/orgs/{org}/admin/forges/{id}/installations", { params: { path: { org: currentOrg(), id: host.id } } })),
    enabled: host.kind === "github",
  });
  const ghRepos = useQuery({
    queryKey: ["install-repos", host.id, install],
    queryFn: async () => (await unwrap(api.GET("/orgs/{org}/admin/forges/{id}/repositories", { params: { path: { org: currentOrg(), id: host.id }, query: { installation: install } } }))).items,
    enabled: host.kind === "github" && !!install,
  });
  const connect = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST("/orgs/{org}/repos", {
          params: { path: { org: currentOrg() } },
          body: {
            forge_host_id: host.id,
            ...(host.kind === "git" ? { clone_url: form.clone_url } : { owner: form.owner, name: form.name }),
            ...(form.token ? { token: form.token } : {}),
            ...(form.target_branch ? { target_branch: form.target_branch } : {}),
            content_root: form.content_root,
          },
        }),
      ),
    onSuccess: async (r) => {
      await qc.invalidateQueries({ queryKey: ["repos"] });
      toast.success(t("admin.connected", { name: r.repo.display_name }));
      onClose();
      await navigate({ to: "/$org/$owner/$repo", params: { org: r.repo.org_slug, owner: r.repo.owner, repo: r.repo.name } });
    },
  });
  return (
    <Dialog open onOpenChange={(v) => !v && onClose()}>
      <DialogContent className="sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>{t("admin.connectTitle", { host: host.display_name })}</DialogTitle>
          <DialogDescription>{t("admin.connectDesc")}</DialogDescription>
        </DialogHeader>
        <form
          className="grid gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            connect.mutate();
          }}
        >
          {host.kind === "git" && (
            <>
              <div className="grid gap-1.5">
                <Label htmlFor="cr-url">{t("admin.cloneUrl")}</Label>
                <Input id="cr-url" className="font-mono" placeholder="https://git.example.com/team/handbook.git" value={form.clone_url} onChange={(e) => setForm({ ...form, clone_url: e.target.value })} />
              </div>
              <div className="grid gap-1.5">
                <Label htmlFor="cr-tok">{t("admin.tokenOptional")}</Label>
                <Input id="cr-tok" type="password" value={form.token} onChange={(e) => setForm({ ...form, token: e.target.value })} />
              </div>
            </>
          )}
          {host.kind === "gitlab" && (
            <>
              <div className="grid gap-1.5">
                <Label htmlFor="cr-path">{t("admin.projectPath")}</Label>
                <Input
                  id="cr-path"
                  className="font-mono"
                  placeholder="platform/runbooks"
                  onChange={(e) => {
                    const v = e.target.value.trim();
                    const i = v.lastIndexOf("/");
                    setForm({ ...form, owner: i > 0 ? v.slice(0, i) : "", name: i > 0 ? v.slice(i + 1) : v });
                  }}
                />
              </div>
              <div className="grid gap-1.5">
                <Label htmlFor="cr-gltok">{t("admin.accessToken")}</Label>
                <Input id="cr-gltok" type="password" value={form.token} onChange={(e) => setForm({ ...form, token: e.target.value })} />
                <span className="text-[0.78125rem] text-muted-foreground">{t("admin.accessTokenHelp")}</span>
              </div>
            </>
          )}
          {host.kind === "github" && (
            <>
              <div className="grid gap-1.5">
                <Label>{t("admin.installation")}</Label>
                <Select value={install} onValueChange={setInstall}>
                  <SelectTrigger>
                    <SelectValue placeholder={installs.isLoading ? t("admin.loading") : t("admin.pickInstallation")} />
                  </SelectTrigger>
                  <SelectContent>
                    {(installs.data?.items ?? []).map((i) => (
                      <SelectItem key={i.external_id} value={i.external_id!}>
                        {i.account_login}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                {installs.data && (
                  <a
                    // In multi mode an org connects accounts through kmdn, which checks the person can see the installation.
                    href={installs.data.connect_url ?? installs.data.install_url}
                    {...(installs.data.connect_url ? {} : { target: "_blank", rel: "noopener noreferrer" })}
                    className="text-[0.78125rem] text-muted-foreground underline underline-offset-4"
                  >
                    {t("admin.installOnAccount")}
                  </a>
                )}
              </div>
              {install && (
                <div className="max-h-56 divide-y overflow-auto rounded-lg border">
                  {(ghRepos.data ?? []).map((r) => (
                    <label key={r.external_id} className={cn("flex cursor-pointer items-center gap-2.5 px-3 py-2 text-[0.84375rem] hover:bg-accent", form.name === r.name && form.owner === r.owner && "bg-accent")}>
                      <input type="radio" name="gh-repo" className="accent-primary" checked={form.name === r.name && form.owner === r.owner} onChange={() => setForm({ ...form, owner: r.owner!, name: r.name! })} />
                      <GitBranch className="size-3.5 text-muted-foreground" />
                      <span className="font-medium">
                        {r.owner}/{r.name}
                      </span>
                      {r.private && <Badge variant="outline">{t("admin.private")}</Badge>}
                    </label>
                  ))}
                  {ghRepos.isLoading && <p className="p-3 text-[0.8125rem] text-muted-foreground">{t("admin.loading")}</p>}
                </div>
              )}
            </>
          )}
          <div className="grid grid-cols-2 gap-3">
            <div className="grid gap-1.5">
              <Label htmlFor="cr-root">{t("settings.contentRoot")}</Label>
              <Input id="cr-root" className="font-mono" placeholder="docs/" value={form.content_root} onChange={(e) => setForm({ ...form, content_root: e.target.value })} />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="cr-branch">{t("settings.targetBranch")}</Label>
              <Input id="cr-branch" className="font-mono" placeholder={t("admin.defaultBranch")} value={form.target_branch} onChange={(e) => setForm({ ...form, target_branch: e.target.value })} />
            </div>
          </div>
          {connect.error && (
            <p role="alert" className="text-[0.8125rem] text-destructive">
              {errorMessage(connect.error, t("errors.generic"))}
            </p>
          )}
          <DialogFooter>
            <Button type="button" variant="ghost" onClick={onClose}>
              {t("settings.cancel")}
            </Button>
            <Button type="submit" disabled={connect.isPending}>
              {connect.isPending && <Loader2 className="animate-spin" />}
              {t("admin.connectAction")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function EmailPanel() {
  const { t } = useTranslation();
  const { data: me } = useMe();
  const smtp = useQuery({ queryKey: ["admin-smtp"], queryFn: () => unwrap(api.GET("/admin/smtp")) });
  return (
    <Panel title={t("admin.email")} desc={t("admin.emailDesc")}>
      <div className="rounded-xl border bg-card shadow-xs">
        {smtp.data && me && <SmtpForm key={smtp.dataUpdatedAt} saved={smtp.data} defaultTestTo={me.email} onDone={() => toast.success(t("settings.saved"))} submitLabel={t("settings.save")} />}
      </div>
    </Panel>
  );
}

type AISettings = components["schemas"]["AISettings"];

/** AI provider: Anthropic or OpenAI-compatible, a model per task, budgets and usage. */
function AIPanel() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const q = useQuery({ queryKey: ["admin-ai"], queryFn: () => unwrap(api.GET("/admin/ai")) });
  const usage = useQuery({ queryKey: ["admin-ai-usage"], queryFn: () => unwrap(api.GET("/admin/ai/usage")) });
  if (!q.data) return <Panel title={t("ai.title")}>{null}</Panel>;
  return <AIForm key={JSON.stringify(q.data)} st={q.data} usage={usage.data} onSaved={(d) => (qc.setQueryData(["admin-ai"], d), void qc.invalidateQueries({ queryKey: ["admin-ai-usage"] }))} />;
}

type AIUsage = paths["/admin/ai/usage"]["get"]["responses"]["200"]["content"]["application/json"];

function AIForm({ st, usage, onSaved }: { st: AISettings; usage?: AIUsage; onSaved: (d: AISettings) => void }) {
  const { t } = useTranslation();
  const [provider, setProvider] = useState(st.provider || "off");
  const [baseURL, setBaseURL] = useState(st.base_url ?? "");
  const [key, setKey] = useState("");
  const [models, setModels] = useState<Record<string, string>>(st.models ?? {});
  const [daily, setDaily] = useState(String(st.user_daily_tokens));
  const [monthly, setMonthly] = useState(String(st.instance_monthly_tokens));
  const [embURL, setEmbURL] = useState(st.embeddings.base_url ?? "");
  const [embModel, setEmbModel] = useState(st.embeddings.model ?? "");
  const [embKey, setEmbKey] = useState("");
  const [scanDays, setScanDays] = useState(String(st.consistency.scan_every_days));
  const [scanCalls, setScanCalls] = useState(String(st.consistency.scan_max_calls));
  const save = useMutation({
    mutationFn: () =>
      unwrap(
        api.PUT("/admin/ai", {
          body: {
            provider: provider === "off" ? "" : (provider as "anthropic" | "openai"),
            base_url: baseURL,
            ...(key ? { api_key: key } : {}),
            models,
            user_daily_tokens: Number(daily) || 0,
            instance_monthly_tokens: Number(monthly) || 0,
            embeddings: { base_url: embURL, model: embModel, ...(embKey ? { api_key: embKey } : {}) },
            consistency: { scan_every_days: Number(scanDays) || 0, scan_max_calls: Number(scanCalls) || 0 },
          },
        }),
      ),
    onSuccess: (d) => {
      onSaved(d);
      if (d.check && !d.check.ok) toast.error(d.check.message);
      else toast.success(t("settings.saved"));
    },
    onError: (e) => toast.error(errorMessage(e, t("errors.generic"))),
  });
  const defaults = (st.defaults as Record<string, Record<string, string>>)[provider] ?? {};
  const fmt = (n: number) => n.toLocaleString();
  return (
    <Panel title={t("ai.title")} desc={t("ai.desc")}>
      {st.disabled && <p className="rounded-lg border border-warning/40 bg-warning/5 p-3 text-[0.84375rem]">{t("ai.disabledByConfig")}</p>}
      {st.managed && <p className="rounded-lg border bg-muted/40 p-3 text-[0.84375rem]">{t("ai.managedByConfig")}</p>}
      <Card
        footer={
          <Button onClick={() => save.mutate()} disabled={save.isPending}>
            {save.isPending && <Loader2 className="animate-spin" />}
            {t("ai.saveCheck")}
          </Button>
        }
      >
        <div className="grid gap-1.5">
          <Label>{t("ai.provider")}</Label>
          <Select value={provider} onValueChange={setProvider} disabled={st.managed}>
            <SelectTrigger className="w-72">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="off">{t("ai.off")}</SelectItem>
              <SelectItem value="anthropic">Anthropic</SelectItem>
              <SelectItem value="openai">{t("ai.openai")}</SelectItem>
            </SelectContent>
          </Select>
        </div>
        {provider !== "off" && (
          <>
            <div className="grid gap-1.5">
              <Label htmlFor="ai-url">{provider === "openai" ? t("ai.baseURL") : t("ai.baseURLOptional")}</Label>
              <Input id="ai-url" disabled={st.managed} value={baseURL} onChange={(e) => setBaseURL(e.target.value)} placeholder={provider === "openai" ? "https://api.openai.com/v1 · http://localhost:11434/v1" : "https://api.anthropic.com"} />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="ai-key">{t("ai.key")}</Label>
              <Input id="ai-key" disabled={st.managed} type="password" autoComplete="off" value={key} onChange={(e) => setKey(e.target.value)} placeholder={st.key_set ? t("ai.keySet") : provider === "openai" ? t("ai.keyOptional") : "sk-ant-…"} />
            </div>
            <div className="grid gap-2">
              <Label>{t("ai.models")}</Label>
              {st.tasks.map((task) => (
                <div key={task} className="grid grid-cols-[10rem_1fr] items-center gap-3 max-sm:grid-cols-1">
                  <span className="text-[0.8125rem] text-muted-foreground">{t(`ai.tasks.${task}`)}</span>
                  <Input disabled={st.managed} aria-label={t(`ai.tasks.${task}`)} value={models[task] ?? ""} onChange={(e) => setModels((m) => ({ ...m, [task]: e.target.value }))} placeholder={defaults[task] ?? (task === "chat" ? t("ai.modelRequired") : t("ai.sameAsChat"))} className="font-mono text-[0.78125rem]" />
                </div>
              ))}
            </div>
            <div className="grid grid-cols-2 gap-3 max-sm:grid-cols-1">
              <div className="grid gap-1.5">
                <Label htmlFor="ai-daily">{t("ai.userDaily")}</Label>
                <Input id="ai-daily" inputMode="numeric" value={daily} onChange={(e) => setDaily(e.target.value.replace(/\D/g, ""))} />
              </div>
              <div className="grid gap-1.5">
                <Label htmlFor="ai-monthly">{t("ai.instanceMonthly")}</Label>
                <Input id="ai-monthly" inputMode="numeric" value={monthly} onChange={(e) => setMonthly(e.target.value.replace(/\D/g, ""))} />
              </div>
            </div>
            <p className="text-[0.78125rem] text-muted-foreground">{t("ai.budgetHint")}</p>
            <div className="grid gap-3 border-t pt-4">
              <div>
                <div className="text-[0.84375rem] font-medium">{t("ai.consistency")}</div>
                <p className="text-[0.78125rem] text-muted-foreground">{t("ai.consistencyHint")}</p>
              </div>
              {st.embeddings.managed && <p className="text-[0.78125rem] text-muted-foreground">{t("ai.embeddingsManaged")}</p>}
              <div className="grid grid-cols-2 gap-3 max-sm:grid-cols-1">
                <div className="grid gap-1.5">
                  <Label htmlFor="ai-emb-url">{t("ai.embeddingsURL")}</Label>
                  <Input id="ai-emb-url" disabled={st.embeddings.managed} value={embURL} onChange={(e) => setEmbURL(e.target.value)} placeholder="https://api.openai.com/v1 · http://localhost:11434/v1" />
                </div>
                <div className="grid gap-1.5">
                  <Label htmlFor="ai-emb-model">{t("ai.embeddingsModel")}</Label>
                  <Input id="ai-emb-model" disabled={st.embeddings.managed} value={embModel} onChange={(e) => setEmbModel(e.target.value)} placeholder="text-embedding-3-small · nomic-embed-text" className="font-mono text-[0.78125rem]" />
                </div>
              </div>
              <div className="grid gap-1.5">
                <Label htmlFor="ai-emb-key">{t("ai.embeddingsKey")}</Label>
                <Input id="ai-emb-key" disabled={st.embeddings.managed} type="password" autoComplete="off" value={embKey} onChange={(e) => setEmbKey(e.target.value)} placeholder={st.embeddings.key_set ? t("ai.keySet") : t("ai.keyOptional")} />
              </div>
              <div className="grid grid-cols-2 gap-3 max-sm:grid-cols-1">
                <div className="grid gap-1.5">
                  <Label htmlFor="ai-scan-days">{t("ai.scanEvery")}</Label>
                  <Input id="ai-scan-days" inputMode="numeric" value={scanDays} onChange={(e) => setScanDays(e.target.value.replace(/\D/g, ""))} />
                </div>
                <div className="grid gap-1.5">
                  <Label htmlFor="ai-scan-calls">{t("ai.scanCalls")}</Label>
                  <Input id="ai-scan-calls" inputMode="numeric" value={scanCalls} onChange={(e) => setScanCalls(e.target.value.replace(/\D/g, ""))} />
                </div>
              </div>
            </div>
          </>
        )}
        {st.check && (
          <p className={cn("flex items-start gap-2 text-[0.8125rem]", st.check.ok ? "text-success" : "text-destructive")}>
            {st.check.ok ? <CheckCircle2 className="mt-0.5 size-4 shrink-0" /> : <TriangleAlert className="mt-0.5 size-4 shrink-0" />}
            <span>
              {st.check.message} <span className="text-muted-foreground">· <Time iso={st.check.at} /></span>
            </span>
          </p>
        )}
      </Card>
      {usage && (
        <Card>
          <div className="flex flex-wrap gap-6 text-[0.84375rem]">
            <div>
              <div className="text-[0.75rem] text-muted-foreground">{t("ai.today")}</div>
              <div className="text-lg font-semibold tabular-nums">{fmt(usage.today_tokens)}</div>
            </div>
            <div>
              <div className="text-[0.75rem] text-muted-foreground">{t("ai.month")}</div>
              <div className="text-lg font-semibold tabular-nums">{fmt(usage.month_tokens)}</div>
            </div>
          </div>
          {usage.by_user.length > 0 && (
            <table className="w-full text-[0.8125rem]">
              <thead>
                <tr className="text-left text-[0.75rem] text-muted-foreground">
                  <th className="py-1 font-medium">{t("ai.byUser")}</th>
                  <th className="py-1 text-right font-medium">{t("ai.runs")}</th>
                  <th className="py-1 text-right font-medium">{t("ai.tokens")}</th>
                </tr>
              </thead>
              <tbody>
                {usage.by_user.map((u) => (
                  <tr key={u.key || "system"} className="border-t">
                    <td className="py-1.5">{u.name || t("ai.system")}</td>
                    <td className="py-1.5 text-right tabular-nums">{u.runs}</td>
                    <td className="py-1.5 text-right tabular-nums">{fmt(u.tokens)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </Card>
      )}
    </Panel>
  );
}
