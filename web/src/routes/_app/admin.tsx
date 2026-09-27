import { useRef, useState } from "react";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { GitBranch, Grid2x2, Loader2, Mail, Plus, ScrollText, Server, Shield, Trash2, UserPlus, Users } from "lucide-react";
import type { components } from "@kmdn/api-client";
import { AppShell } from "@/components/shell/app-shell";
import { TopBar } from "@/components/shell/top-bar";
import { Card, Panel, SettingsLayout } from "@/components/settings-layout";
import { Avatar } from "@/components/avatar";
import { Time } from "@/components/time";
import { ForgeIcon } from "@/components/shell/forge-icon";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { api, errorMessage, unwrap, useMe } from "@/lib/api";
import { reposQuery, useRepos } from "@/lib/repos";
import { cn } from "@/lib/utils";
import { SmtpForm } from "@/components/smtp-form";

type AdminUser = components["schemas"]["AdminUser"];
type ForgeHost = components["schemas"]["ForgeHost"];

const SECTIONS = ["users", "groups", "repositories", "email", "audit"] as const;
type Section = (typeof SECTIONS)[number];

export const Route = createFileRoute("/_app/admin")({
  validateSearch: (s: Record<string, unknown>): { section?: Section; forge_error?: string } => ({
    ...(SECTIONS.includes(s.section as Section) ? { section: s.section as Section } : {}),
    ...(typeof s.forge_error === "string" ? { forge_error: s.forge_error } : {}),
  }),
  component: Admin,
});

function Admin() {
  const { t } = useTranslation();
  const { data: me } = useMe();
  const { section = "users", forge_error } = Route.useSearch();
  return (
    <AppShell>
      {(controls) => (
        <>
          <TopBar controls={controls} title={t("admin.title")} />
          <div className="min-h-0 flex-1 overflow-auto">
            {!me?.is_instance_admin ? (
              <p className="p-8 text-muted-foreground">{t("admin.only")}</p>
            ) : (
              <SettingsLayout
                title={t("admin.instance")}
                active={section}
                sections={[
                  { key: "users", label: t("admin.users"), icon: Users },
                  { key: "groups", label: t("admin.groups"), icon: Grid2x2 },
                  { key: "repositories", label: t("admin.repositories"), icon: Server },
                  { key: "email", label: t("admin.email"), icon: Mail },
                  { key: "audit", label: t("admin.audit"), icon: ScrollText },
                ]}
                linkProps={(key) => ({ to: "/admin", search: { section: key as Section } })}
              >
                {forge_error && (
                  <p role="alert" className="mb-4 rounded-lg border border-destructive/30 bg-destructive/5 p-3 text-[13.5px]">
                    {t("admin.forgeError", { code: forge_error })}
                  </p>
                )}
                {section === "users" && <UsersPanel />}
                {section === "groups" && <GroupsPanel />}
                {section === "repositories" && <ReposPanel />}
                {section === "email" && <EmailPanel />}
                {section === "audit" && <AuditPanel />}
              </SettingsLayout>
            )}
          </div>
        </>
      )}
    </AppShell>
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
        <table className="w-full text-[13.5px]">
          <thead className="text-left text-[13px] text-muted-foreground">
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
                      <div className="flex items-center gap-1.5 font-medium">
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
                <td className="px-3 py-2.5 text-right whitespace-nowrap">
                  <Button variant="ghost" size="sm" onClick={() => update.mutate({ id: u.id, body: { is_instance_admin: !u.is_instance_admin } })}>
                    <Shield />
                    {u.is_instance_admin ? t("admin.removeAdmin") : t("admin.makeAdmin")}
                  </Button>
                  <Button variant="ghost" size="sm" onClick={() => update.mutate({ id: u.id, body: { status: u.status === "active" ? "deactivated" : "active" } })}>
                    {u.status === "active" ? t("admin.deactivate") : t("admin.reactivate")}
                  </Button>
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
    mutationFn: () => unwrap(api.POST("/admin/invites", { body: { email, ...(repoID !== "none" ? { repo_id: repoID, role } : {}) } })),
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
          {invite.error && <p className="text-[13px] text-destructive">{errorMessage(invite.error, t("errors.generic"))}</p>}
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
  const groups = useQuery({ queryKey: ["admin-groups"], queryFn: async () => (await unwrap(api.GET("/admin/groups"))).items });
  const create = useMutation({
    mutationFn: () => unwrap(api.POST("/admin/groups", { body: { name } })),
    onSuccess: () => {
      setName("");
      void qc.invalidateQueries({ queryKey: ["admin-groups"] });
    },
    onError: (e) => toast.error(errorMessage(e, t("errors.generic"))),
  });
  const del = useMutation({
    mutationFn: (id: string) => unwrap(api.DELETE("/admin/groups/{id}", { params: { path: { id } } })),
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
  const members = useQuery({ queryKey: key, queryFn: async () => (await unwrap(api.GET("/admin/groups/{id}/members", { params: { path: { id: groupID } } }))).items });
  const found = useQuery({ queryKey: ["users", q], queryFn: async () => (await unwrap(api.GET("/users", { params: { query: { q } } }))).items, enabled: q.trim().length > 1 });
  const add = useMutation({
    mutationFn: (user: string) => unwrap(api.PUT("/admin/groups/{id}/members/{user}", { params: { path: { id: groupID, user } } })),
    onSuccess: (d) => {
      qc.setQueryData(key, d.items);
      setQ("");
      void qc.invalidateQueries({ queryKey: ["admin-groups"] });
    },
  });
  const remove = useMutation({
    mutationFn: (user: string) => unwrap(api.DELETE("/admin/groups/{id}/members/{user}", { params: { path: { id: groupID, user } } })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: key });
      void qc.invalidateQueries({ queryKey: ["admin-groups"] });
    },
  });
  return (
    <div className="grid gap-2 border-t bg-muted/30 px-4 py-3">
      {(members.data ?? []).map((u) => (
        <div key={u.id} className="flex items-center gap-2.5 text-[13.5px]">
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
              <button key={u.id} type="button" className="flex w-full items-center gap-2.5 px-3 py-2 text-left text-[13.5px] hover:bg-accent" onClick={() => add.mutate(u.id)}>
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
  const hosts = useQuery({ queryKey: ["forges"], queryFn: async () => (await unwrap(api.GET("/admin/forges"))).items });
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
        <table className="w-full text-[13.5px]">
          <thead className="text-left text-[13px] text-muted-foreground">
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
                  <Link to="/$owner/$repo/settings" params={{ owner: r.owner, repo: r.name }} className="flex items-center gap-2 font-medium hover:underline">
                    <ForgeIcon kind={r.forge_kind} className="size-3.5" />
                    {r.display_name}
                  </Link>
                </td>
                <td className="px-3 py-2.5 font-mono text-[12.5px] max-md:hidden">{r.scope.root ? `${r.scope.root}/` : "/"}</td>
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
  const formRef = useRef<HTMLFormElement>(null);
  const [target, setTarget] = useState<{ action: string; manifest: string } | null>(null);
  const [open, setOpen] = useState(false);
  const [base, setBase] = useState("");
  const [org, setOrg] = useState("");
  const start = useMutation({
    mutationFn: () => unwrap(api.POST("/admin/forges/github/manifest", { body: { base_url: base || undefined, organization: org || undefined } })),
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
            {start.error && <p className="text-[13px] text-destructive">{errorMessage(start.error, t("errors.generic"))}</p>}
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
  const [form, setForm] = useState({ base_url: "https://gitlab.com", client_id: "", client_secret: "" });
  const add = useMutation({
    mutationFn: () => unwrap(api.POST("/admin/forges", { body: { kind: "gitlab", ...form } })),
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
            <Input id="gl-url" value={form.base_url} onChange={(e) => setForm({ ...form, base_url: e.target.value })} />
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="gl-id">{t("admin.oauthClientId")}</Label>
            <Input id="gl-id" value={form.client_id} onChange={(e) => setForm({ ...form, client_id: e.target.value })} />
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="gl-secret">{t("admin.oauthClientSecret")}</Label>
            <Input id="gl-secret" type="password" value={form.client_secret} onChange={(e) => setForm({ ...form, client_secret: e.target.value })} />
          </div>
          {add.error && <p className="text-[13px] text-destructive">{errorMessage(add.error, t("errors.generic"))}</p>}
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
    queryFn: () => unwrap(api.GET("/admin/forges/{id}/installations", { params: { path: { id: host.id } } })),
    enabled: host.kind === "github",
  });
  const ghRepos = useQuery({
    queryKey: ["install-repos", host.id, install],
    queryFn: async () => (await unwrap(api.GET("/admin/forges/{id}/repositories", { params: { path: { id: host.id }, query: { installation: install } } }))).items,
    enabled: host.kind === "github" && !!install,
  });
  const connect = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST("/repos", {
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
      await qc.invalidateQueries({ queryKey: reposQuery.queryKey });
      toast.success(t("admin.connected", { name: r.repo.display_name }));
      onClose();
      await navigate({ to: "/$owner/$repo", params: { owner: r.repo.owner, repo: r.repo.name } });
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
                <span className="text-[12.5px] text-muted-foreground">{t("admin.accessTokenHelp")}</span>
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
                  <a href={installs.data.install_url} target="_blank" rel="noopener noreferrer" className="text-[12.5px] text-muted-foreground underline underline-offset-4">
                    {t("admin.installOnAccount")}
                  </a>
                )}
              </div>
              {install && (
                <div className="max-h-56 divide-y overflow-auto rounded-lg border">
                  {(ghRepos.data ?? []).map((r) => (
                    <label key={r.external_id} className={cn("flex cursor-pointer items-center gap-2.5 px-3 py-2 text-[13.5px] hover:bg-accent", form.name === r.name && form.owner === r.owner && "bg-accent")}>
                      <input type="radio" name="gh-repo" className="accent-primary" checked={form.name === r.name && form.owner === r.owner} onChange={() => setForm({ ...form, owner: r.owner!, name: r.name! })} />
                      <GitBranch className="size-3.5 text-muted-foreground" />
                      <span className="font-medium">
                        {r.owner}/{r.name}
                      </span>
                      {r.private && <Badge variant="outline">{t("admin.private")}</Badge>}
                    </label>
                  ))}
                  {ghRepos.isLoading && <p className="p-3 text-[13px] text-muted-foreground">{t("admin.loading")}</p>}
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
            <p role="alert" className="text-[13px] text-destructive">
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

function AuditPanel() {
  const { t } = useTranslation();
  const audit = useQuery({ queryKey: ["audit"], queryFn: async () => (await unwrap(api.GET("/admin/audit"))).items });
  return (
    <Panel title={t("admin.audit")} desc={t("admin.auditDesc")}>
      <div className="overflow-x-auto rounded-xl border">
        <table className="w-full text-[13.5px]">
          <thead className="text-left text-[13px] text-muted-foreground">
            <tr className="border-b">
              <th className="px-3 py-2.5 font-medium">{t("admin.time")}</th>
              <th className="px-3 py-2.5 font-medium">{t("admin.action")}</th>
              <th className="px-3 py-2.5 font-medium max-md:hidden">{t("admin.target")}</th>
            </tr>
          </thead>
          <tbody>
            {(audit.data ?? []).map((e) => (
              <tr key={e.id} className="border-b last:border-0">
                <td className="px-3 py-2 whitespace-nowrap text-muted-foreground">
                  <Time iso={e.at} />
                </td>
                <td className="px-3 py-2">
                  <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs">{e.action}</code>
                </td>
                <td className="px-3 py-2 text-muted-foreground max-md:hidden">{[e.target_type, e.target_id].filter(Boolean).join(" ")}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </Panel>
  );
}
