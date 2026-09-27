import { useState } from "react";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { AlertTriangle, CheckCircle2, Copy, FileCode, Loader2, RefreshCw, Settings, ShieldCheck, Trash2, UserPlus, Users, Webhook, X } from "lucide-react";
import { AppShell } from "@/components/shell/app-shell";
import { TopBar } from "@/components/shell/top-bar";
import { Card, Panel, Row, SettingsLayout } from "@/components/settings-layout";
import { Avatar } from "@/components/avatar";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { api, errorMessage, unwrap } from "@/lib/api";
import { Time } from "@/components/time";
import { cn } from "@/lib/utils";
import type { components } from "@kmdn/api-client";
import { atLeast, type Member, type RepoView, type Role } from "@/lib/repos";
import { useRepo } from "@/lib/use-repo";

const SECTIONS = ["general", "members", "hooks", "danger"] as const;
type Section = (typeof SECTIONS)[number];

export const Route = createFileRoute("/_app/$owner/$repo/settings")({
  validateSearch: (s: Record<string, unknown>): { section?: Section } => (SECTIONS.includes(s.section as Section) ? { section: s.section as Section } : {}),
  component: RepoSettings,
});

function RepoSettings() {
  const { t } = useTranslation();
  const repo = useRepo();
  const { section = "general" } = Route.useSearch();
  const params = Route.useParams();
  return (
    <AppShell repo={repo}>
      {(controls) => (
        <>
          <TopBar controls={controls} title={`${repo.display_name} · ${t("settings.title")}`} />
          <div className="min-h-0 flex-1 overflow-auto">
            {!atLeast(repo.role, "admin") ? (
              <p className="p-8 text-muted-foreground">{t("settings.adminOnly")}</p>
            ) : (
              <SettingsLayout
                title={repo.slug}
                active={section}
                sections={[
                  { key: "general", label: t("settings.general"), icon: Settings },
                  { key: "members", label: t("settings.members"), icon: Users },
                  { key: "hooks", label: t("hooks.title"), icon: Webhook },
                  { key: "danger", label: t("settings.danger"), icon: AlertTriangle, danger: true },
                ]}
                linkProps={(key) => ({ to: "/$owner/$repo/settings", params, search: { section: key as Section } })}
              >
                {section === "general" && <General key={repo.id + repo.head_sha} repo={repo} />}
                {section === "members" && <Members repo={repo} />}
                {section === "hooks" && <Hooks repo={repo} />}
                {section === "danger" && <Danger repo={repo} />}
              </SettingsLayout>
            )}
          </div>
        </>
      )}
    </AppShell>
  );
}

function lines(s: string) {
  return s
    .split("\n")
    .map((l) => l.trim())
    .filter(Boolean);
}

function General({ repo }: { repo: RepoView }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const fromFile = new Set(repo.scope.from_file ?? []);
  const [form, setForm] = useState({
    display_name: repo.display_name,
    target_branch: repo.target_branch,
    content_root: repo.content_root,
    include: repo.include.join("\n"),
    exclude: repo.exclude.join("\n"),
    allow_self_approval: !!repo.settings.allow_self_approval,
  });
  const save = useMutation({
    mutationFn: () =>
      unwrap(
        api.PATCH("/repos/{repo}", {
          params: { path: { repo: repo.id } },
          body: {
            display_name: form.display_name,
            target_branch: form.target_branch,
            content_root: form.content_root,
            include: lines(form.include),
            exclude: lines(form.exclude),
            settings: { ...repo.settings, allow_self_approval: form.allow_self_approval },
          },
        }),
      ),
    onSuccess: async () => {
      toast.success(t("settings.saved"));
      await qc.invalidateQueries({ queryKey: ["repo"] });
      await qc.invalidateQueries({ queryKey: ["repos"] });
      await qc.invalidateQueries({ queryKey: ["tree", repo.id] });
    },
  });
  const webhook = useQuery({
    queryKey: ["webhook", repo.id],
    queryFn: () => unwrap(api.GET("/repos/{repo}/webhook", { params: { path: { repo: repo.id } } })),
    enabled: repo.forge_kind !== "github",
  });
  const locked = (k: string) =>
    fromFile.has(k) ? (
      <span className="mt-1 flex items-center gap-1 text-[12.5px] text-muted-foreground">
        <FileCode className="size-3" />
        {t("settings.fromFile")}
      </span>
    ) : null;
  return (
    <Panel title={t("settings.general")} desc={t("settings.generalDesc")}>
      <Card
        footer={
          <Button onClick={() => save.mutate()} disabled={save.isPending}>
            {save.isPending && <Loader2 className="animate-spin" />}
            {t("settings.save")}
          </Button>
        }
      >
        {fromFile.size > 0 && (
          <p className="flex gap-2 rounded-lg border border-info/30 bg-info/5 p-3 text-[13.5px]">
            <FileCode className="mt-0.5 size-4 text-info" />
            <span>{t("settings.kmdnYml", { fields: [...fromFile].join(", ") })}</span>
          </p>
        )}
        {repo.kmdn_yml?.error ? (
          <p className="rounded-lg border border-destructive/30 bg-destructive/5 p-3 text-[13.5px]">{t("settings.kmdnYmlError", { error: String(repo.kmdn_yml.error) })}</p>
        ) : null}
        <div className="grid gap-4 sm:grid-cols-2">
          <div className="grid gap-1.5">
            <Label htmlFor="rs-name">{t("settings.displayName")}</Label>
            <Input id="rs-name" value={form.display_name} onChange={(e) => setForm({ ...form, display_name: e.target.value })} />
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="rs-branch">{t("settings.targetBranch")}</Label>
            <Input id="rs-branch" className="font-mono" value={form.target_branch} onChange={(e) => setForm({ ...form, target_branch: e.target.value })} />
            <span className="text-[12.5px] text-muted-foreground">{t("settings.targetBranchHelp")}</span>
          </div>
        </div>
        <div className="grid gap-1.5">
          <Label htmlFor="rs-root">{t("settings.contentRoot")}</Label>
          <Input id="rs-root" className="font-mono" placeholder="docs/" disabled={fromFile.has("root")} value={fromFile.has("root") ? repo.scope.root : form.content_root} onChange={(e) => setForm({ ...form, content_root: e.target.value })} />
          {locked("root") ?? <span className="text-[12.5px] text-muted-foreground">{t("settings.contentRootHelp")}</span>}
        </div>
        <div className="grid gap-4 sm:grid-cols-2">
          <div className="grid gap-1.5">
            <Label htmlFor="rs-inc">{t("settings.include")}</Label>
            <Textarea id="rs-inc" rows={3} className="font-mono text-[13px]" placeholder={"**/*.md"} disabled={fromFile.has("include")} value={fromFile.has("include") ? repo.scope.include.join("\n") : form.include} onChange={(e) => setForm({ ...form, include: e.target.value })} />
            {locked("include")}
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="rs-exc">{t("settings.exclude")}</Label>
            <Textarea id="rs-exc" rows={3} className="font-mono text-[13px]" disabled={fromFile.has("exclude")} value={fromFile.has("exclude") ? repo.scope.exclude.join("\n") : form.exclude} onChange={(e) => setForm({ ...form, exclude: e.target.value })} />
            {locked("exclude")}
          </div>
        </div>
        <Row title={t("settings.selfApproval")} desc={t("settings.selfApprovalDesc")}>
          <Switch checked={form.allow_self_approval} onCheckedChange={(v) => setForm({ ...form, allow_self_approval: v })} aria-label={t("settings.selfApproval")} />
        </Row>
        {save.error && (
          <p role="alert" className="text-[13px] text-destructive">
            {errorMessage(save.error, t("errors.generic"))}
          </p>
        )}
      </Card>
      <Card>
        <div className="flex items-start gap-3">
          <ShieldCheck className="mt-0.5 size-4 text-muted-foreground" />
          <div className="text-[13.5px]">
            <div className="font-medium">{repo.protection.protected ? t("settings.protected") : t("settings.notProtected")}</div>
            <div className="text-muted-foreground">{repo.protection.detail || t("settings.protectionUnknown")}</div>
          </div>
        </div>
      </Card>
      {webhook.data?.url && (
        <Card>
          <div className="flex items-start gap-3">
            <Webhook className="mt-0.5 size-4 text-muted-foreground" />
            <div className="grid min-w-0 flex-1 gap-2 text-[13.5px]">
              <div className="font-medium">{t("settings.webhook")}</div>
              <div className="text-muted-foreground">{repo.forge_kind === "gitlab" ? t("settings.webhookGitlab") : t("settings.webhookGit")}</div>
              <CopyField label="URL" value={webhook.data.url} />
              <CopyField label={webhook.data.header} value={webhook.data.secret} secret />
            </div>
          </div>
        </Card>
      )}
    </Panel>
  );
}

function CopyField({ label, value, secret }: { label: string; value: string; secret?: boolean }) {
  const [show, setShow] = useState(!secret);
  return (
    <div className="flex items-center gap-2 rounded-lg border bg-muted/50 py-1 pr-1 pl-3 font-mono text-[12.5px]">
      <span className="shrink-0 text-muted-foreground">{label}</span>
      <span className="min-w-0 flex-1 truncate">{show ? value : "•".repeat(24)}</span>
      {secret && (
        <Button variant="ghost" size="sm" className="h-7" onClick={() => setShow((v) => !v)}>
          {show ? "Hide" : "Show"}
        </Button>
      )}
      <Button
        variant="ghost"
        size="icon"
        className="size-7"
        aria-label={`Copy ${label}`}
        onClick={() => void navigator.clipboard?.writeText(value).then(() => toast.success("Copied"))}
      >
        <Copy />
      </Button>
    </div>
  );
}

const ROLES: Exclude<Role, "">[] = ["viewer", "contributor", "maintainer", "admin"];

function RoleSelect({ value, onChange, disabled }: { value: Role; onChange: (r: Exclude<Role, "">) => void; disabled?: boolean }) {
  const { t } = useTranslation();
  return (
    <Select value={value} onValueChange={(v) => onChange(v as Exclude<Role, "">)} disabled={disabled}>
      <SelectTrigger className="h-8 w-[150px] text-[13px]" aria-label={t("settings.role")}>
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {ROLES.map((r) => (
          <SelectItem key={r} value={r}>
            {t(`roles.${r}`)}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}

function Members({ repo }: { repo: RepoView }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const key = ["members", repo.id];
  const members = useQuery({ queryKey: key, queryFn: async () => (await unwrap(api.GET("/repos/{repo}/members", { params: { path: { repo: repo.id } } }))).items });
  const setRole = useMutation({
    mutationFn: (v: { type: "user" | "group"; id: string; role: Exclude<Role, ""> }) =>
      unwrap(api.PUT("/repos/{repo}/members/{type}/{id}", { params: { path: { repo: repo.id, type: v.type, id: v.id } }, body: { role: v.role } })),
    onSuccess: (d) => qc.setQueryData(key, d.items),
    onError: (e) => toast.error(errorMessage(e, t("errors.generic"))),
  });
  const remove = useMutation({
    mutationFn: (m: Member) => unwrap(api.DELETE("/repos/{repo}/members/{type}/{id}", { params: { path: { repo: repo.id, type: m.type, id: m.id } } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: key }),
  });
  return (
    <Panel title={t("settings.members")} desc={t("settings.membersDesc")}>
      <AddMember repo={repo} onAdded={() => void qc.invalidateQueries({ queryKey: key })} />
      <div className="overflow-hidden rounded-xl border">
        <table className="w-full text-[13.5px]">
          <thead className="text-left text-[13px] text-muted-foreground">
            <tr className="border-b">
              <th className="px-3 py-2.5 font-medium">{t("settings.member")}</th>
              <th className="px-3 py-2.5 font-medium">{t("settings.role")}</th>
              <th className="w-10" />
            </tr>
          </thead>
          <tbody>
            {(members.data ?? []).map((m) => (
              <tr key={m.type + m.id} className="border-b last:border-0">
                <td className="px-3 py-2.5">
                  <div className="flex items-center gap-2.5">
                    {m.type === "user" ? (
                      <Avatar name={m.name} id={m.id} size="md" />
                    ) : (
                      <span className="grid size-7 place-items-center rounded-full bg-muted">
                        <Users className="size-3.5 text-muted-foreground" />
                      </span>
                    )}
                    <div className="min-w-0">
                      <div className="font-medium">{m.name}</div>
                      <div className="truncate text-xs text-muted-foreground">{m.type === "user" ? m.email : t("settings.groupMembers", { count: m.members ?? 0 })}</div>
                    </div>
                  </div>
                </td>
                <td className="px-3 py-2.5">
                  <RoleSelect value={m.role} onChange={(role) => setRole.mutate({ type: m.type, id: m.id, role })} />
                </td>
                <td className="px-2">
                  <Button variant="ghost" size="icon" className="size-8" aria-label={t("settings.remove", { name: m.name })} onClick={() => remove.mutate(m)}>
                    <X />
                  </Button>
                </td>
              </tr>
            ))}
            {members.data?.length === 0 && (
              <tr>
                <td colSpan={3} className="px-3 py-6 text-center text-muted-foreground">
                  {t("settings.noMembers")}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
      <p className="text-[12.5px] text-muted-foreground">{t("settings.rolesHelp")}</p>
    </Panel>
  );
}

function AddMember({ repo, onAdded }: { repo: RepoView; onAdded: () => void }) {
  const { t } = useTranslation();
  const [q, setQ] = useState("");
  const [role, setRole] = useState<Exclude<Role, "">>("contributor");
  const users = useQuery({ queryKey: ["users", q], queryFn: async () => (await unwrap(api.GET("/users", { params: { query: { q } } }))).items, enabled: q.trim().length > 1 });
  const groups = useQuery({ queryKey: ["groups"], queryFn: async () => (await unwrap(api.GET("/groups"))).items });
  const matchedGroups = (groups.data ?? []).filter((g) => q.trim().length > 1 && g.name.toLowerCase().includes(q.trim().toLowerCase()));
  const grant = useMutation({
    mutationFn: (v: { type: "user" | "group"; id: string }) => unwrap(api.PUT("/repos/{repo}/members/{type}/{id}", { params: { path: { repo: repo.id, type: v.type, id: v.id } }, body: { role } })),
    onSuccess: () => {
      setQ("");
      onAdded();
      toast.success(t("settings.added"));
    },
  });
  const invite = useMutation({
    mutationFn: () => unwrap(api.POST("/repos/{repo}/invites", { params: { path: { repo: repo.id } }, body: { email: q.trim(), role } })),
    onSuccess: (r) => {
      setQ("");
      onAdded();
      toast.success(r.status === "invited" ? t("settings.invited", { email: r.email }) : t("settings.added"));
    },
    onError: (e) => toast.error(errorMessage(e, t("errors.generic"))),
  });
  const looksLikeEmail = /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(q.trim());
  return (
    <Card>
      <div className="flex flex-wrap items-center gap-2">
        <div className="relative min-w-[220px] flex-1">
          <Input placeholder={t("settings.addPlaceholder")} value={q} onChange={(e) => setQ(e.target.value)} aria-label={t("settings.addPlaceholder")} />
          {q.trim().length > 1 && (
            <div className="absolute inset-x-0 top-full z-10 mt-1 overflow-hidden rounded-lg border bg-popover shadow-md">
              {(users.data ?? []).map((u) => (
                <button key={u.id} type="button" className="flex w-full items-center gap-2.5 px-3 py-2 text-left text-[13.5px] hover:bg-accent" onClick={() => grant.mutate({ type: "user", id: u.id })}>
                  <Avatar name={u.name} id={u.id} size="sm" />
                  <span className="font-medium">{u.name}</span>
                  <span className="truncate text-muted-foreground">{u.email}</span>
                </button>
              ))}
              {matchedGroups.map((g) => (
                <button key={g.id} type="button" className="flex w-full items-center gap-2.5 px-3 py-2 text-left text-[13.5px] hover:bg-accent" onClick={() => grant.mutate({ type: "group", id: g.id })}>
                  <Users className="size-4 text-muted-foreground" />
                  <span className="font-medium">{g.name}</span>
                  <span className="text-muted-foreground">{t("settings.groupMembers", { count: g.members })}</span>
                </button>
              ))}
              {looksLikeEmail && !(users.data ?? []).some((u) => u.email === q.trim().toLowerCase()) && (
                <button type="button" className="flex w-full items-center gap-2.5 px-3 py-2 text-left text-[13.5px] hover:bg-accent" onClick={() => invite.mutate()}>
                  <UserPlus className="size-4 text-muted-foreground" />
                  {t("settings.inviteEmail", { email: q.trim() })}
                </button>
              )}
              {!users.isFetching && (users.data?.length ?? 0) === 0 && matchedGroups.length === 0 && !looksLikeEmail && <div className="px-3 py-2 text-[13px] text-muted-foreground">{t("settings.noMatch")}</div>}
            </div>
          )}
        </div>
        <RoleSelect value={role} onChange={setRole} />
      </div>
    </Card>
  );
}

function Danger({ repo }: { repo: RepoView }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [confirm, setConfirm] = useState(false);
  const [typed, setTyped] = useState("");
  const refresh = useMutation({
    mutationFn: () => unwrap(api.POST("/repos/{repo}/refresh", { params: { path: { repo: repo.id } } })),
    onSuccess: () => toast.success(t("settings.refreshing")),
  });
  const disconnect = useMutation({
    mutationFn: () => unwrap(api.DELETE("/repos/{repo}", { params: { path: { repo: repo.id } } })),
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: ["repos"] });
      toast.success(t("settings.disconnected"));
      await navigate({ to: "/" });
    },
  });
  return (
    <Panel title={t("settings.danger")} desc={t("settings.dangerDesc")}>
      <Card className="border-destructive/30">
        <Row title={t("settings.resync")} desc={t("settings.resyncDesc")}>
          <Button variant="outline" size="sm" onClick={() => refresh.mutate()} disabled={refresh.isPending}>
            {refresh.isSuccess ? <CheckCircle2 /> : <RefreshCw />}
            {t("settings.resyncAction")}
          </Button>
        </Row>
        <Row title={t("settings.disconnect")} desc={t("settings.disconnectDesc", { slug: repo.slug })}>
          <Button variant="destructive" size="sm" onClick={() => setConfirm(true)}>
            <Trash2 />
            {t("settings.disconnectAction")}
          </Button>
        </Row>
      </Card>
      <Dialog open={confirm} onOpenChange={setConfirm}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("settings.disconnectTitle", { name: repo.display_name })}</DialogTitle>
            <DialogDescription>{t("settings.disconnectConfirm", { slug: repo.slug })}</DialogDescription>
          </DialogHeader>
          <Input value={typed} onChange={(e) => setTyped(e.target.value)} placeholder={repo.slug} aria-label={t("settings.typeToConfirm")} className="font-mono" />
          <DialogFooter>
            <Button variant="ghost" onClick={() => setConfirm(false)}>
              {t("settings.cancel")}
            </Button>
            <Button variant="destructive" disabled={typed !== repo.slug || disconnect.isPending} onClick={() => disconnect.mutate()}>
              {t("settings.disconnectAction")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Panel>
  );
}

type Hook = components["schemas"]["Hook"];

/** Outgoing webhooks and Slack messages (docs/specs/11-api.md#outgoing-webhooks). */
function Hooks({ repo }: { repo: RepoView }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const q = useQuery({ queryKey: ["hooks", repo.id], queryFn: () => unwrap(api.GET("/repos/{repo}/hooks", { params: { path: { repo: repo.id } } })) });
  const [adding, setAdding] = useState(false);
  const [secret, setSecret] = useState<string | null>(null);
  const [log, setLog] = useState<Hook | null>(null);
  const refresh = () => void qc.invalidateQueries({ queryKey: ["hooks", repo.id] });
  const onError = (e: unknown) => toast.error(errorMessage(e, t("errors.generic")));
  const patch = useMutation({
    mutationFn: ({ h, active }: { h: Hook; active: boolean }) => unwrap(api.PATCH("/repos/{repo}/hooks/{hook}", { params: { path: { repo: repo.id, hook: h.id } }, body: { active } })),
    onSuccess: refresh,
    onError,
  });
  const remove = useMutation({
    mutationFn: (h: Hook) => unwrap(api.DELETE("/repos/{repo}/hooks/{hook}", { params: { path: { repo: repo.id, hook: h.id } } })),
    onSuccess: refresh,
    onError,
  });
  const ping = useMutation({
    mutationFn: (h: Hook) => unwrap(api.POST("/repos/{repo}/hooks/{hook}/ping", { params: { path: { repo: repo.id, hook: h.id } } })),
    onSuccess: () => {
      toast.success(t("hooks.pinged"));
      setTimeout(refresh, 1500);
    },
    onError,
  });
  const hooks = q.data?.items ?? [];
  return (
    <Panel
      title={t("hooks.title")}
      desc={t("hooks.desc")}
      actions={
        <Button size="sm" onClick={() => setAdding(true)}>
          <Webhook />
          {t("hooks.add")}
        </Button>
      }
    >
      {secret && (
        <Card>
          <p className="text-[13.5px] font-medium">{t("hooks.secretOnce")}</p>
          <CopyField label="secret" value={secret} secret />
          <p className="text-[12.5px] text-muted-foreground">{t("hooks.secretHint")}</p>
          <Button size="sm" variant="outline" className="justify-self-start" onClick={() => setSecret(null)}>
            {t("hooks.secretSaved")}
          </Button>
        </Card>
      )}
      {q.data && hooks.length === 0 && <p className="text-[13.5px] text-muted-foreground">{t("hooks.empty")}</p>}
      {hooks.map((h) => (
        <Card key={h.id}>
          <div className="flex flex-wrap items-center gap-3">
            <Webhook className="size-4 text-muted-foreground" />
            <div className="min-w-0 flex-1">
              <div className="truncate text-sm font-medium">
                {t(`hooks.kinds.${h.kind}`)} · <span className="font-mono text-[12.5px]">{h.url_host}</span>
              </div>
              <div className="mt-0.5 flex flex-wrap gap-1">
                {h.events.map((e) => (
                  <span key={e} className="rounded bg-muted px-1.5 py-0.5 font-mono text-[11px] text-muted-foreground">
                    {e}
                  </span>
                ))}
              </div>
            </div>
            <Switch checked={h.active} onCheckedChange={(v) => patch.mutate({ h, active: v })} aria-label={t("hooks.active")} />
          </div>
          <div className="flex flex-wrap items-center gap-2 text-[12.5px] text-muted-foreground">
            {h.last_delivery ? (
              <span className={cn(h.last_delivery.status === "failed" && "text-destructive", h.last_delivery.status === "delivered" && "text-success")}>
                {t(`hooks.status.${h.last_delivery.status}`)} · {h.last_delivery.event}
                {h.last_delivery.response_code ? ` · ${h.last_delivery.response_code}` : ""}
              </span>
            ) : (
              <span>{t("hooks.noDeliveries")}</span>
            )}
            <span className="ml-auto flex gap-1">
              <Button size="sm" variant="ghost" onClick={() => ping.mutate(h)} disabled={ping.isPending}>
                {t("hooks.ping")}
              </Button>
              <Button size="sm" variant="ghost" onClick={() => setLog(h)}>
                {t("hooks.deliveries")}
              </Button>
              <Button size="sm" variant="ghost" className="text-destructive" onClick={() => remove.mutate(h)} aria-label={t("hooks.delete")}>
                <Trash2 />
              </Button>
            </span>
          </div>
        </Card>
      ))}
      <AddHook repo={repo} events={q.data?.events ?? []} open={adding} onOpenChange={setAdding} onCreated={(s) => (setSecret(s), refresh())} />
      {log && <DeliveryLog repo={repo} hook={log} onClose={() => setLog(null)} />}
    </Panel>
  );
}

function AddHook({ repo, events, open, onOpenChange, onCreated }: { repo: RepoView; events: string[]; open: boolean; onOpenChange: (v: boolean) => void; onCreated: (secret: string | null) => void }) {
  const { t } = useTranslation();
  const [kind, setKind] = useState<"generic" | "slack">("slack");
  const [url, setUrl] = useState("");
  const [picked, setPicked] = useState<string[]>(["revision.submitted", "revision.approved", "revision.published"]);
  const create = useMutation({
    mutationFn: () => unwrap(api.POST("/repos/{repo}/hooks", { params: { path: { repo: repo.id } }, body: { kind, url, events: picked } })),
    onSuccess: (r) => {
      onOpenChange(false);
      setUrl("");
      onCreated(r.secret ?? null);
    },
    onError: (e) => toast.error(errorMessage(e, t("errors.generic"))),
  });
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("hooks.add")}</DialogTitle>
          <DialogDescription>{t(`hooks.kindHelp.${kind}`)}</DialogDescription>
        </DialogHeader>
        <form
          className="grid gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            create.mutate();
          }}
        >
          <div className="grid gap-1.5">
            <Label>{t("hooks.kind")}</Label>
            <Select value={kind} onValueChange={(v) => setKind(v as "generic" | "slack")}>
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="slack">{t("hooks.kinds.slack")}</SelectItem>
                <SelectItem value="generic">{t("hooks.kinds.generic")}</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="hook-url">{t("hooks.url")}</Label>
            <Input id="hook-url" required value={url} onChange={(e) => setUrl(e.target.value)} placeholder={kind === "slack" ? "https://hooks.slack.com/services/…" : "https://example.com/kmdn"} />
          </div>
          <fieldset className="grid gap-2">
            <legend className="mb-1 text-sm font-medium">{t("hooks.events")}</legend>
            {events.map((e) => (
              <label key={e} className="flex items-center gap-2 text-[13.5px]">
                <input type="checkbox" className="accent-primary" checked={picked.includes(e)} onChange={(x) => setPicked((p) => (x.target.checked ? [...p, e] : p.filter((y) => y !== e)))} />
                <span>{t(`hooks.eventNames.${e.replace(".", "_")}`, { defaultValue: e })}</span>
                <span className="font-mono text-[11px] text-muted-foreground">{e}</span>
              </label>
            ))}
          </fieldset>
          <DialogFooter>
            <Button type="button" variant="ghost" onClick={() => onOpenChange(false)}>
              {t("common.cancel")}
            </Button>
            <Button type="submit" disabled={!url.trim() || !picked.length || create.isPending}>
              {create.isPending && <Loader2 className="animate-spin" />}
              {t("hooks.create")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function DeliveryLog({ repo, hook, onClose }: { repo: RepoView; hook: Hook; onClose: () => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const q = useQuery({
    queryKey: ["hook-deliveries", hook.id],
    queryFn: async () => (await unwrap(api.GET("/repos/{repo}/hooks/{hook}/deliveries", { params: { path: { repo: repo.id, hook: hook.id } } }))).items,
    refetchInterval: 5000,
  });
  const redeliver = useMutation({
    mutationFn: (id: string) => unwrap(api.POST("/repos/{repo}/hooks/{hook}/deliveries/{delivery}/redeliver", { params: { path: { repo: repo.id, hook: hook.id, delivery: id } } })),
    onSuccess: () => setTimeout(() => void qc.invalidateQueries({ queryKey: ["hook-deliveries", hook.id] }), 1000),
    onError: (e) => toast.error(errorMessage(e, t("errors.generic"))),
  });
  return (
    <Dialog open onOpenChange={(v) => !v && onClose()}>
      <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-[720px]">
        <DialogHeader>
          <DialogTitle>{t("hooks.deliveries")}</DialogTitle>
          <DialogDescription className="font-mono text-[12.5px]">{hook.url_host}</DialogDescription>
        </DialogHeader>
        {q.data && q.data.length === 0 && <p className="text-[13.5px] text-muted-foreground">{t("hooks.noDeliveries")}</p>}
        <ul className="divide-y rounded-lg border text-[13px]">
          {(q.data ?? []).map((d) => (
            <li key={d.id} className="flex flex-wrap items-center gap-3 px-3 py-2">
              <span className={cn("size-2 shrink-0 rounded-full", d.status === "delivered" ? "bg-success" : d.status === "failed" ? "bg-destructive" : "bg-warning")} />
              <span className="font-mono text-[12px]">{d.event}</span>
              <span className="text-muted-foreground">
                {t(`hooks.status.${d.status}`)}
                {d.response_code ? ` · ${d.response_code}` : ""} · {t("hooks.attempts", { count: d.attempts })}
              </span>
              {d.error && <span className="w-full truncate text-[12px] text-destructive">{d.error}</span>}
              <Time iso={d.created_at} className="ml-auto text-[12px] text-muted-foreground" />
              <Button size="sm" variant="ghost" className="h-7" onClick={() => redeliver.mutate(d.id)} disabled={redeliver.isPending}>
                {t("hooks.redeliver")}
              </Button>
            </li>
          ))}
        </ul>
      </DialogContent>
    </Dialog>
  );
}
