import { Fragment, useState } from "react";
import { createFileRoute } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { KeyRound, Loader2, LogOut, Monitor, Moon, Sun } from "lucide-react";
import { AppShell } from "@/components/shell/app-shell";
import { TopBar } from "@/components/shell/top-bar";
import { Card, Panel, Row } from "@/components/settings-layout";
import { Avatar } from "@/components/avatar";
import { Time } from "@/components/time";
import { ForgeIcon } from "@/components/shell/forge-icon";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api, errorMessage, meQuery, unwrap, useMe } from "@/lib/api";
import { Switch } from "@/components/ui/switch";
import { currentSubscription, disablePush, enablePush, pushSupported } from "@/lib/notifications";
import type { components } from "@kmdn/api-client";

type NotificationPref = components["schemas"]["NotificationPref"];
import { cancelled, createPasskey, passkeysSupported } from "@/lib/passkeys";
import { useTheme } from "@/lib/theme";
import { cn } from "@/lib/utils";
import { PALETTES, UI_SCALES, type ThemeChoice } from "@/theme";

export const Route = createFileRoute("/_app/settings/profile")({
  validateSearch: (s: Record<string, unknown>): { link_error?: string } => (typeof s.link_error === "string" ? { link_error: s.link_error } : {}),
  component: Profile,
});

function Profile() {
  const { t } = useTranslation();
  const { data: me } = useMe();
  const { link_error } = Route.useSearch();
  if (!me) return null;
  return (
    <AppShell>
      {(controls) => (
        <>
          <TopBar controls={controls} title={t("profile.title")} />
          <div className="min-h-0 flex-1 overflow-auto">
            <div className="mx-auto grid max-w-[48.75rem] gap-8 px-8 pt-7 pb-20 max-md:px-4">
              {link_error && (
                <p role="alert" className="rounded-lg border border-destructive/30 bg-destructive/5 p-3 text-[0.84375rem]">
                  {t(`profile.linkError.${link_error}`, { defaultValue: t("profile.linkError.generic") })}
                </p>
              )}
              <ProfileCard key={me.id + me.name} />
              <CommitEmail />
              <LinkedAccounts />
              <Passkeys />
              <Appearance />
              <Notifications />
              <Sessions />
            </div>
          </div>
        </>
      )}
    </AppShell>
  );
}

function ProfileCard() {
  const { t } = useTranslation();
  const { data: me } = useMe();
  const qc = useQueryClient();
  const [name, setName] = useState(me!.name);
  const save = useMutation({
    mutationFn: () => unwrap(api.PATCH("/me", { body: { name } })),
    onSuccess: (u) => {
      qc.setQueryData(meQuery.queryKey, u);
      toast.success(t("settings.saved"));
    },
  });
  return (
    <Panel title={t("profile.title")} desc={t("profile.desc")}>
      <Card
        footer={
          <Button onClick={() => save.mutate()} disabled={save.isPending || name.trim() === me!.name}>
            {t("settings.save")}
          </Button>
        }
      >
        <div className="flex flex-wrap items-center gap-4">
          <Avatar name={me!.name} id={me!.id} size="lg" />
          <div className="grid min-w-[13.75rem] flex-1 gap-4 sm:grid-cols-2">
            <div className="grid gap-1.5">
              <Label htmlFor="pf-name">{t("profile.name")}</Label>
              <Input id="pf-name" value={name} onChange={(e) => setName(e.target.value)} />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="pf-email">{t("profile.email")}</Label>
              <Input id="pf-email" value={me!.email} readOnly />
            </div>
          </div>
        </div>
      </Card>
    </Panel>
  );
}

function CommitEmail() {
  const { t } = useTranslation();
  const { data: me } = useMe();
  const qc = useQueryClient();
  const linked = useQuery({ queryKey: ["linked"], queryFn: async () => (await unwrap(api.GET("/me/linked-accounts"))).items });
  const [mode, setMode] = useState(me!.commit_email_mode);
  const [custom, setCustom] = useState("");
  const save = useMutation({
    mutationFn: () => unwrap(api.PUT("/me/commit-email", { body: { mode, ...(mode === "custom" ? { custom } : {}) } })),
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: meQuery.queryKey });
      toast.success(t("settings.saved"));
    },
    onError: (e) => toast.error(errorMessage(e, t("errors.generic"))),
  });
  const noreply = linked.data?.[0]?.noreply_email;
  const opt = (value: typeof mode, title: string, desc: string, mono: boolean) => (
    <label className={cn("flex cursor-pointer gap-3 rounded-lg border p-3", mode === value && "border-foreground ring-1 ring-foreground")}>
      <input type="radio" name="commit-email" className="mt-1 accent-primary" checked={mode === value} onChange={() => setMode(value)} />
      <span className="min-w-0">
        <span className="block text-sm font-medium">{title}</span>
        <span className={cn("block text-[0.78125rem] break-all text-muted-foreground", mono && "font-mono")}>{desc}</span>
      </span>
    </label>
  );
  return (
    <Panel title={t("profile.commitEmail")} desc={t("profile.commitEmailDesc")}>
      <Card
        footer={
          <Button onClick={() => save.mutate()} disabled={save.isPending}>
            {t("settings.save")}
          </Button>
        }
      >
        <div className="grid gap-2">
          {opt("forge_noreply", t("profile.forgeNoreply"), noreply ?? t("profile.linkFirst"), !!noreply)}
          {opt("account", t("profile.accountEmail"), me!.email, true)}
          {opt("custom", t("profile.customEmail"), t("profile.customEmailDesc"), false)}
          {mode === "custom" && <Input type="email" placeholder="you@example.com" value={custom} onChange={(e) => setCustom(e.target.value)} aria-label={t("profile.customEmail")} />}
        </div>
      </Card>
    </Panel>
  );
}

function LinkedAccounts() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const linked = useQuery({ queryKey: ["linked"], queryFn: async () => (await unwrap(api.GET("/me/linked-accounts"))).items });
  const providers = useQuery({ queryKey: ["oauth-providers"], queryFn: async () => (await unwrap(api.GET("/auth/oauth/providers"))).items });
  const unlink = useMutation({
    mutationFn: (id: string) => unwrap(api.DELETE("/me/linked-accounts/{id}", { params: { path: { id } } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["linked"] }),
  });
  const linkedHosts = new Set((linked.data ?? []).map((a) => a.forge_host_id));
  return (
    <Panel title={t("profile.linked")} desc={t("profile.linkedDesc")}>
      <Card>
        {(linked.data ?? []).map((a) => (
          <Row key={a.id} title={`${a.host_name} · @${a.login}`} desc={t("profile.linkedOn", { date: new Date(a.linked_at).toLocaleDateString() })}>
            <Button variant="outline" size="sm" onClick={() => unlink.mutate(a.id)}>
              {t("profile.unlink")}
            </Button>
          </Row>
        ))}
        {(providers.data ?? [])
          .filter((p) => !linkedHosts.has(p.id))
          .map((p) => (
            <Row key={p.id} title={p.display_name} desc={t("profile.notLinked")}>
              <Button asChild size="sm">
                <a href={`/api/v1/auth/oauth/${p.id}/start?mode=link&redirect=/settings/profile`}>
                  <ForgeIcon kind={p.kind} />
                  {t("profile.link", { name: p.display_name })}
                </a>
              </Button>
            </Row>
          ))}
        {(providers.data?.length ?? 0) === 0 && (linked.data?.length ?? 0) === 0 && <p className="text-[0.84375rem] text-muted-foreground">{t("profile.noProviders")}</p>}
      </Card>
    </Panel>
  );
}

function Notifications() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const prefs = useQuery({ queryKey: ["notification-prefs"], queryFn: () => unwrap(api.GET("/me/notification-prefs")) });
  const here = useQuery({ queryKey: ["push-here"], queryFn: async () => !!(await currentSubscription()), enabled: pushSupported() });
  const save = useMutation({
    mutationFn: (items: NotificationPref[]) => unwrap(api.PUT("/me/notification-prefs", { body: { items } })),
    onSuccess: (d) => qc.setQueryData(["notification-prefs"], d),
    onError: (e) => toast.error(errorMessage(e, t("errors.generic"))),
  });
  const toggle = useMutation({
    mutationFn: async (on: boolean) => {
      if (on && !(await enablePush())) throw new Error(t(Notification.permission === "denied" ? "inbox.prefs.pushDenied" : "inbox.prefs.pushUnsupported"));
      if (!on) await disablePush();
    },
    onSettled: () => {
      void qc.invalidateQueries({ queryKey: ["push-here"] });
      void qc.invalidateQueries({ queryKey: ["notification-prefs"] });
    },
    onError: (e) => toast.error(errorMessage(e, t("errors.generic"))),
  });
  const set = (kind: string, field: "in_app" | "push", v: boolean) => {
    const items = (prefs.data?.items ?? []).map((p) => (p.kind === kind ? { ...p, [field]: v } : p));
    save.mutate(items.filter((p) => p.kind === kind));
    qc.setQueryData(["notification-prefs"], { ...prefs.data!, items });
  };
  return (
    <Panel
      title={t("inbox.prefs.title")}
      desc={t("inbox.prefs.hint")}
      actions={
        prefs.data?.push_available &&
        (pushSupported() ? (
          <Button size="sm" variant={here.data ? "outline" : "default"} disabled={toggle.isPending} onClick={() => toggle.mutate(!here.data)}>
            {toggle.isPending && <Loader2 className="animate-spin" />}
            {here.data ? t("inbox.prefs.disablePush") : t("inbox.prefs.enablePush")}
          </Button>
        ) : (
          <span className="text-[0.78125rem] text-muted-foreground">{t("inbox.prefs.pushUnsupported")}</span>
        ))
      }
    >
      <Card>
        <div className="grid grid-cols-[1fr_auto_auto] items-center gap-x-6 gap-y-3 text-[0.84375rem]">
          <span className="text-[0.75rem] font-medium text-muted-foreground">{t("inbox.prefs.kind")}</span>
          <span className="text-[0.75rem] font-medium text-muted-foreground">{t("inbox.prefs.inApp")}</span>
          <span className="text-[0.75rem] font-medium text-muted-foreground">{t("inbox.prefs.push")}</span>
          {(prefs.data?.items ?? []).map((p) => (
            <Fragment key={p.kind}>
              <span>{t(`inbox.prefKinds.${p.kind}`)}</span>
              <Switch checked={p.in_app} onCheckedChange={(v) => set(p.kind, "in_app", v)} aria-label={`${t(`inbox.prefKinds.${p.kind}`)}: ${t("inbox.prefs.inApp")}`} />
              <Switch checked={p.push} disabled={!here.data} onCheckedChange={(v) => set(p.kind, "push", v)} aria-label={`${t(`inbox.prefKinds.${p.kind}`)}: ${t("inbox.prefs.push")}`} />
            </Fragment>
          ))}
        </div>
        {here.data && <p className="text-[0.78125rem] text-muted-foreground">{t("inbox.prefs.pushOn")}</p>}
      </Card>
    </Panel>
  );
}

function Appearance() {
  const { t } = useTranslation();
  const { choice, setChoice, palette, setPalette, scale, setScale, resolved } = useTheme();
  const opts: { v: ThemeChoice; label: string; icon: typeof Sun }[] = [
    { v: "system", label: t("shell.themeSystem"), icon: Monitor },
    { v: "light", label: t("shell.themeLight"), icon: Sun },
    { v: "dark", label: t("shell.themeDark"), icon: Moon },
  ];
  const seg = (on: boolean) => cn("flex h-8 items-center gap-1.5 rounded-md px-3 text-[0.84375rem] font-medium text-muted-foreground", on && "bg-background text-foreground shadow-xs");
  return (
    <Panel title={t("profile.appearance")} desc={t("profile.appearanceDesc")}>
      <Card>
        <div className="grid gap-5">
          <div className="grid gap-2">
            <Label>{t("profile.palette")}</Label>
            <div role="radiogroup" aria-label={t("profile.palette")} className="grid grid-cols-[repeat(auto-fill,minmax(9.5rem,1fr))] gap-2.5">
              {PALETTES.map((p) => (
                <button
                  key={p}
                  type="button"
                  role="radio"
                  aria-checked={palette === p}
                  onClick={() => setPalette(p)}
                  className={cn("grid gap-2 rounded-xl border p-2 text-left outline-offset-2", palette === p ? "border-foreground ring-1 ring-foreground" : "hover:bg-accent")}
                >
                  {/* The palette's own tokens, in the current mode. */}
                  <div data-palette={p} data-theme={resolved} className="grid gap-1.5 rounded-lg border bg-background p-2 text-foreground">
                    <div className="flex items-center gap-1.5">
                      <span className="size-2.5 rounded-full bg-primary" />
                      <span className="h-1.5 flex-1 rounded-full bg-muted" />
                    </div>
                    <div className="flex items-baseline justify-between">
                      <span className="text-[1rem] font-semibold">Aa</span>
                      <span className="flex gap-1">
                        <span className="size-2 rounded-full bg-success" />
                        <span className="size-2 rounded-full bg-warning" />
                        <span className="size-2 rounded-full bg-info" />
                        <span className="size-2 rounded-full bg-destructive" />
                      </span>
                    </div>
                    <span className="h-1.5 w-2/3 rounded-full bg-muted-foreground/40" />
                  </div>
                  <span className="px-0.5 text-[0.84375rem] font-medium">{t(`profile.palettes.${p}`)}</span>
                </button>
              ))}
            </div>
          </div>
          <div className="grid gap-2">
            <Label>{t("shell.theme")}</Label>
            <div role="radiogroup" aria-label={t("shell.theme")} className="inline-flex w-fit gap-0.5 rounded-lg bg-muted p-1">
              {opts.map((o) => (
                <button key={o.v} type="button" role="radio" aria-checked={choice === o.v} onClick={() => setChoice(o.v)} className={seg(choice === o.v)}>
                  <o.icon className="size-3.5" />
                  {o.label}
                </button>
              ))}
            </div>
          </div>
          <div className="grid gap-2">
            <Label>{t("profile.uiScale")}</Label>
            <div role="radiogroup" aria-label={t("profile.uiScale")} className="inline-flex w-fit gap-0.5 rounded-lg bg-muted p-1">
              {UI_SCALES.map((s) => (
                <button key={s} type="button" role="radio" aria-checked={scale === s} onClick={() => setScale(s)} className={seg(scale === s)}>
                  {s}%{s === 120 && <span className="text-muted-foreground">· {t("profile.default")}</span>}
                </button>
              ))}
            </div>
          </div>
        </div>
      </Card>
    </Panel>
  );
}

function Passkeys() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const list = useQuery({ queryKey: ["passkeys"], queryFn: async () => (await unwrap(api.GET("/me/passkeys"))).items });
  const [name, setName] = useState("");
  const [renaming, setRenaming] = useState<{ id: string; name: string } | null>(null);
  const refresh = () => void qc.invalidateQueries({ queryKey: ["passkeys"] });
  const add = useMutation({
    mutationFn: async () => {
      const o = await unwrap(api.POST("/me/passkeys/register/options", { body: { name: name.trim() || defaultPasskeyName() } }));
      const credential = await createPasskey(o.options.publicKey);
      return unwrap(api.POST("/me/passkeys/register/verify", { body: { ceremony: o.ceremony, credential } }));
    },
    onSuccess: () => (setName(""), refresh(), toast.success(t("profile.passkeyAdded"))),
    onError: (e) => !cancelled(e) && toast.error(errorMessage(e, t("errors.generic"))),
  });
  const rename = useMutation({
    mutationFn: (r: { id: string; name: string }) => unwrap(api.PATCH("/me/passkeys/{id}", { params: { path: { id: r.id } }, body: { name: r.name } })),
    onSuccess: () => (setRenaming(null), refresh()),
    onError: (e) => toast.error(errorMessage(e, t("errors.generic"))),
  });
  const remove = useMutation({
    mutationFn: (id: string) => unwrap(api.DELETE("/me/passkeys/{id}", { params: { path: { id } } })),
    onSuccess: refresh,
    onError: (e) => toast.error(errorMessage(e, t("errors.generic"))),
  });
  const supported = passkeysSupported();
  return (
    <Panel title={t("profile.passkeys")} desc={t("profile.passkeysDesc")}>
      <Card>
        {(list.data ?? []).map((p) =>
          renaming?.id === p.id ? (
            <form
              key={p.id}
              className="flex items-center gap-2 border-b py-3 last:border-b-0 first:pt-0"
              onSubmit={(e) => (e.preventDefault(), rename.mutate(renaming))}
            >
              <Input value={renaming.name} onChange={(e) => setRenaming({ ...renaming, name: e.target.value })} maxLength={60} autoFocus aria-label={t("profile.passkeyName")} />
              <Button size="sm" type="submit" disabled={!renaming.name.trim() || rename.isPending}>
                {t("common.save")}
              </Button>
              <Button size="sm" variant="ghost" type="button" onClick={() => setRenaming(null)}>
                {t("common.cancel")}
              </Button>
            </form>
          ) : (
            <Row
              key={p.id}
              title={p.name}
              desc={
                <>
                  {t("profile.passkeyAddedOn", { date: new Date(p.created_at).toLocaleDateString() })}
                  {" · "}
                  {p.last_used_at ? (
                    <>
                      {t("profile.lastUsed")} <Time iso={p.last_used_at} />
                    </>
                  ) : (
                    t("profile.neverUsed")
                  )}
                  {p.synced && ` · ${t("profile.synced")}`}
                </>
              }
            >
              <span className="flex gap-1">
                <Button variant="ghost" size="sm" onClick={() => setRenaming({ id: p.id, name: p.name })}>
                  {t("profile.rename")}
                </Button>
                <Button variant="ghost" size="sm" onClick={() => remove.mutate(p.id)} disabled={remove.isPending}>
                  {t("profile.remove")}
                </Button>
              </span>
            </Row>
          ),
        )}
        {list.data?.length === 0 && <p className="text-[0.84375rem] text-muted-foreground">{t("profile.noPasskeys")}</p>}
        {supported ? (
          <form className="flex flex-wrap items-center gap-2" onSubmit={(e) => (e.preventDefault(), add.mutate())}>
            <Input value={name} onChange={(e) => setName(e.target.value)} placeholder={defaultPasskeyName()} maxLength={60} className="w-56" aria-label={t("profile.passkeyName")} />
            <Button type="submit" size="sm" disabled={add.isPending}>
              {add.isPending ? <Loader2 className="animate-spin" /> : <KeyRound />}
              {t("profile.addPasskey")}
            </Button>
          </form>
        ) : (
          <p className="text-[0.8125rem] text-muted-foreground">{t("profile.passkeysUnsupported")}</p>
        )}
      </Card>
    </Panel>
  );
}

/** A name for a new passkey from the device, e.g. "Chrome on macOS". */
function defaultPasskeyName(): string {
  return typeof navigator === "undefined" ? "Passkey" : summarizeUA(navigator.userAgent);
}

function Sessions() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const sessions = useQuery({ queryKey: ["sessions"], queryFn: async () => (await unwrap(api.GET("/me/sessions"))).items });
  const revoke = useMutation({
    mutationFn: (id: string) => unwrap(api.DELETE("/me/sessions/{id}", { params: { path: { id } } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["sessions"] }),
  });
  return (
    <Panel title={t("profile.sessions")} desc={t("profile.sessionsDesc")}>
      <Card>
        {(sessions.data ?? []).map((s) => (
          <Row
            key={s.id}
            title={s.user_agent ? summarizeUA(s.user_agent) : t("profile.unknownDevice")}
            desc={
              <>
                {s.ip} · {t("profile.lastSeen")} <Time iso={s.last_seen_at} />
                {s.current && <b className="ml-1 font-medium text-foreground">· {t("profile.thisDevice")}</b>}
              </>
            }
          >
            {!s.current && (
              <Button variant="ghost" size="sm" onClick={() => revoke.mutate(s.id)} disabled={revoke.isPending}>
                {revoke.isPending ? <Loader2 className="animate-spin" /> : <LogOut />}
                {t("profile.signOutDevice")}
              </Button>
            )}
          </Row>
        ))}
      </Card>
    </Panel>
  );
}

export function summarizeUA(ua: string): string {
  const browser = /Edg\//.test(ua) ? "Edge" : /Firefox\//.test(ua) ? "Firefox" : /Chrome\//.test(ua) ? "Chrome" : /Safari\//.test(ua) ? "Safari" : "Browser";
  const os = /Mac OS X|Macintosh/.test(ua) ? "macOS" : /Windows/.test(ua) ? "Windows" : /Android/.test(ua) ? "Android" : /iPhone|iPad/.test(ua) ? "iOS" : /Linux/.test(ua) ? "Linux" : "";
  return os ? `${browser} on ${os}` : browser;
}
