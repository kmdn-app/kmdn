import { useEffect, useState, type FormEvent, type ReactNode } from "react";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Check, CheckCircle2, Loader2, Mail, Send, ArrowRight, AlertCircle } from "lucide-react";
import { Logo } from "@/components/logo";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Separator } from "@/components/ui/separator";
import { api, errorMessage, refreshMe, setupStatusQuery, unwrap, useMe, useSetupStatus } from "@/lib/api";
import { cn } from "@/lib/utils";
import type { components } from "@kmdn/api-client";

type SMTPInput = components["schemas"]["SMTPInput"];

export const Route = createFileRoute("/setup")({
  validateSearch: (s: Record<string, unknown>): { token?: string } => (typeof s.token === "string" ? { token: s.token } : {}),
  component: Setup,
});

const STEPS = ["admin", "email", "forge", "ai", "done"] as const;
type Step = (typeof STEPS)[number];

function Setup() {
  const { t } = useTranslation();
  const { data: status } = useSetupStatus();
  const { data: me } = useMe();
  const navigate = useNavigate();
  const [chosen, setStep] = useState<Step | null>(null);
  const initial: Step = !status?.admin_exists ? "admin" : status.smtp_configured ? "forge" : "email";
  const step: Step = chosen && !(chosen === "admin" && status?.admin_exists) ? chosen : initial;

  useEffect(() => {
    if (status && !status.needed) void navigate({ to: "/", replace: true });
  }, [status, navigate]);

  useEffect(() => {
    if (status?.admin_exists && me === null) void navigate({ to: "/signin", search: { redirect: "/setup" }, replace: true });
  }, [status, me, navigate]);

  if (!status) return null;
  if (status.admin_exists && me && !me.is_instance_admin) {
    return (
      <Frame step={step} total={STEPS.length}>
        <Panel title={t("setup.title")} desc="Setup is in progress. An instance admin needs to finish it." />
      </Frame>
    );
  }
  const next = (s: Step) => setStep(STEPS[Math.min(STEPS.indexOf(s) + 1, STEPS.length - 1)]!);
  const prev = (s: Step) => setStep(STEPS[Math.max(STEPS.indexOf(s) - 1, 1)]!);

  return (
    <Frame step={step} total={STEPS.length}>
      {step === "admin" && <AdminStep onDone={() => next("admin")} />}
      {step === "email" && <EmailStep onDone={() => next("email")} />}
      {step === "forge" && (
        <Panel title={t("setup.forge.title")} desc={t("setup.forge.desc")}>
          <Footer onBack={() => prev("forge")}>
            <Button onClick={() => next("forge")}>
              {t("setup.forge.skip")}
              <ArrowRight />
            </Button>
          </Footer>
        </Panel>
      )}
      {step === "ai" && (
        <Panel title={t("setup.ai.title")} desc={t("setup.ai.desc")}>
          <Footer onBack={() => prev("ai")}>
            <Button onClick={() => next("ai")}>
              {t("setup.ai.skip")}
              <ArrowRight />
            </Button>
          </Footer>
        </Panel>
      )}
      {step === "done" && <DoneStep onBack={() => prev("done")} />}
    </Frame>
  );
}

function Frame({ step, total, children }: { step: Step; total: number; children: ReactNode }) {
  const { t } = useTranslation();
  const idx = STEPS.indexOf(step);
  return (
    <div className="min-h-full bg-sidebar px-4 py-12">
      <div className="mx-auto w-full max-w-[880px]">
        <div className="mb-6 flex items-center gap-2.5">
          <Logo />
          <div>
            <div className="font-semibold">{t("setup.title")}</div>
            <div className="text-xs text-muted-foreground">{window.location.host} · first run</div>
          </div>
          <Badge variant="outline" className="ml-auto">
            {t("setup.step", { n: idx + 1, total })}
          </Badge>
        </div>
        <div className="grid gap-6 md:grid-cols-[200px_minmax(0,1fr)]">
          <ol className="flex flex-wrap gap-0.5 md:flex-col" aria-label="Setup steps">
            {STEPS.map((s, i) => {
              const done = i < idx;
              const cur = i === idx;
              return (
                <li
                  key={s}
                  aria-current={cur ? "step" : undefined}
                  className={cn(
                    "flex items-center gap-2.5 rounded-lg px-2.5 py-2 text-[13.5px] text-muted-foreground",
                    done && "text-foreground",
                    cur && "bg-accent font-medium text-foreground",
                  )}
                >
                  <span
                    className={cn(
                      "grid size-[22px] place-items-center rounded-full border bg-background text-[11.5px] font-semibold",
                      done && "border-primary bg-primary text-primary-foreground",
                      cur && "border-foreground",
                    )}
                  >
                    {done ? <Check className="size-3" /> : i + 1}
                  </span>
                  {t(`setup.steps.${s}`)}
                </li>
              );
            })}
          </ol>
          <div className="rounded-xl border bg-card shadow-xs">{children}</div>
        </div>
      </div>
    </div>
  );
}

function Panel({ title, desc, icon, children }: { title: string; desc: string; icon?: ReactNode; children?: ReactNode }) {
  return (
    <div>
      <div className="px-5 pt-4.5">
        <h2 className="flex items-center gap-2 text-[15px] font-semibold tracking-tight">
          {icon}
          {title}
        </h2>
        <p className="mt-0.5 text-[13px] text-muted-foreground">{desc}</p>
      </div>
      {children}
    </div>
  );
}

function Footer({ onBack, children }: { onBack?: () => void; children: ReactNode }) {
  const { t } = useTranslation();
  return (
    <div className="mt-4 flex items-center gap-2 border-t px-5 py-3">
      {onBack && (
        <Button variant="ghost" onClick={onBack} type="button">
          {t("setup.back")}
        </Button>
      )}
      <span className="flex-1" />
      {children}
    </div>
  );
}

function Field({ id, label, help, children }: { id: string; label: string; help?: string; children: ReactNode }) {
  return (
    <div className="grid gap-1.5">
      <Label htmlFor={id}>{label}</Label>
      {children}
      {help && <p className="text-[12.5px] text-muted-foreground">{help}</p>}
    </div>
  );
}

function AdminStep({ onDone }: { onDone: () => void }) {
  const { t } = useTranslation();
  const { token } = Route.useSearch();
  const qc = useQueryClient();
  const [form, setForm] = useState({ name: "", email: "", instance_name: "" });
  const create = useMutation({
    mutationFn: () => unwrap(api.POST("/setup/admin", { body: { token: token ?? "", ...form } })),
    onSuccess: async () => {
      window.history.replaceState(null, "", "/setup");
      await Promise.all([refreshMe(qc), qc.fetchQuery({ ...setupStatusQuery, staleTime: 0 })]);
      onDone();
    },
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    create.mutate();
  };
  return (
    <Panel title={t("setup.admin.title")} desc={t("setup.admin.desc")}>
      <form onSubmit={submit}>
        <div className="grid gap-4 px-5 pt-4">
          {!token && (
            <p role="alert" className="flex gap-2 rounded-lg border border-warning/40 bg-warning/10 p-3 text-[13.5px]">
              <AlertCircle className="mt-0.5 size-4 shrink-0 text-warning" />
              {t("setup.admin.missingToken")}
            </p>
          )}
          <div className="grid gap-4 sm:grid-cols-2">
            <Field id="setup-name" label={t("setup.admin.name")}>
              <Input id="setup-name" autoComplete="name" required value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} />
            </Field>
            <Field id="setup-email" label={t("setup.admin.email")}>
              <Input id="setup-email" type="email" autoComplete="email" required value={form.email} onChange={(e) => setForm({ ...form, email: e.target.value })} />
            </Field>
          </div>
          <Field id="setup-instance" label={t("setup.admin.instance")} help={t("setup.admin.instanceHelp")}>
            <Input id="setup-instance" value={form.instance_name} onChange={(e) => setForm({ ...form, instance_name: e.target.value })} />
          </Field>
          {create.error && (
            <p role="alert" className="text-[13px] text-destructive">
              {errorMessage(create.error, t("errors.generic"))}
            </p>
          )}
        </div>
        <Footer>
          <Button type="submit" disabled={!token || create.isPending || !form.name.trim() || !form.email.trim()}>
            {create.isPending && <Loader2 className="animate-spin" />}
            {t("setup.admin.submit")}
          </Button>
        </Footer>
      </form>
    </Panel>
  );
}

function EmailStep({ onDone }: { onDone: () => void }) {
  const { data: me } = useMe();
  const saved = useQuery({ queryKey: ["admin-smtp"], queryFn: () => unwrap(api.GET("/admin/smtp")) });
  if (!saved.data || !me) return null;
  return <EmailForm saved={saved.data} defaultTestTo={me.email} onDone={onDone} />;
}

function EmailForm({ saved, defaultTestTo, onDone }: { saved: components["schemas"]["SMTPSettings"]; defaultTestTo: string; onDone: () => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [form, setForm] = useState<SMTPInput>(() => ({
    host: saved.host,
    port: saved.port || 587,
    security: (saved.security || "starttls") as SMTPInput["security"],
    username: saved.username,
    from: saved.from,
  }));
  const [password, setPassword] = useState("");
  const [testTo, setTestTo] = useState(defaultTestTo);

  const locked = saved.from_config;
  const input = (): SMTPInput => ({ ...form, ...(password ? { password } : {}) });
  const test = useMutation({
    mutationFn: () => unwrap(api.POST("/admin/smtp/test", { body: { to: testTo, ...(locked ? {} : { settings: input() }) } })),
  });
  const save = useMutation({
    mutationFn: async () => {
      if (!locked) await unwrap(api.PUT("/admin/smtp", { body: input() }));
    },
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: setupStatusQuery.queryKey });
      onDone();
    },
  });
  const set = (k: keyof SMTPInput) => (e: React.ChangeEvent<HTMLInputElement | HTMLSelectElement>) =>
    setForm({ ...form, [k]: k === "port" ? Number(e.target.value) : e.target.value });

  return (
    <Panel title={t("setup.email.title")} desc={t("setup.email.desc")} icon={<Mail className="size-4" />}>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          save.mutate();
        }}
      >
        <fieldset disabled={locked} className="grid gap-4 px-5 pt-4">
          {locked && <p className="rounded-lg border bg-muted p-3 text-[13.5px]">{t("setup.email.locked")}</p>}
          <div className="grid gap-4 sm:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
            <Field id="smtp-host" label={t("setup.email.host")}>
              <Input id="smtp-host" placeholder="smtp.postmarkapp.com" value={form.host} onChange={set("host")} />
            </Field>
            <div className="grid grid-cols-2 gap-4">
              <Field id="smtp-port" label={t("setup.email.port")}>
                <Input id="smtp-port" inputMode="numeric" className="tabular-nums" value={String(form.port)} onChange={set("port")} />
              </Field>
              <Field id="smtp-security" label={t("setup.email.security")}>
                <select
                  id="smtp-security"
                  className="h-9 rounded-md border border-input bg-background px-3 text-sm shadow-xs"
                  value={form.security}
                  onChange={set("security")}
                >
                  <option value="starttls">STARTTLS</option>
                  <option value="tls">TLS</option>
                  <option value="none">None</option>
                </select>
              </Field>
            </div>
          </div>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field id="smtp-user" label={t("setup.email.username")}>
              <Input id="smtp-user" autoComplete="off" value={form.username} onChange={set("username")} />
            </Field>
            <Field id="smtp-pass" label={t("setup.email.password")} help={saved.has_password ? t("setup.email.passwordKeep") : undefined}>
              <Input id="smtp-pass" type="password" autoComplete="new-password" value={password} onChange={(e) => setPassword(e.target.value)} />
            </Field>
          </div>
          <Field id="smtp-from" label={t("setup.email.from")} help={t("setup.email.fromHelp")}>
            <Input id="smtp-from" placeholder="Docs <docs@example.com>" value={form.from} onChange={set("from")} />
          </Field>
        </fieldset>
        <div className="px-5 pt-4">
          <Separator className="mb-4" />
          <Label htmlFor="smtp-test-to" className="mb-1.5">
            {t("setup.email.testTo")}
          </Label>
          <div className="flex flex-wrap gap-2">
            <Input id="smtp-test-to" type="email" className="min-w-[200px] flex-1" value={testTo} onChange={(e) => setTestTo(e.target.value)} />
            <Button type="button" variant="outline" onClick={() => test.mutate()} disabled={test.isPending || (!locked && !form.host)}>
              {test.isPending ? <Loader2 className="animate-spin" /> : <Send />}
              {t("setup.email.test")}
            </Button>
          </div>
          {test.data && (
            <p
              role="status"
              className={cn(
                "mt-3 flex items-start gap-2 rounded-lg border p-3 text-[13.5px]",
                test.data.ok ? "border-success/30 bg-success/10" : "border-destructive/30 bg-destructive/10",
              )}
            >
              {test.data.ok ? <CheckCircle2 className="mt-0.5 size-4 text-success" /> : <AlertCircle className="mt-0.5 size-4 text-destructive" />}
              <span>
                {test.data.ok ? t("setup.email.testOk", { to: test.data.to }) : t("setup.email.testFailed", { error: test.data.error })}
                {!test.data.ok && test.data.hint && <span className="block text-muted-foreground">{test.data.hint}</span>}
              </span>
            </p>
          )}
          {save.error && (
            <p role="alert" className="mt-3 text-[13px] text-destructive">
              {errorMessage(save.error, t("errors.generic"))}
            </p>
          )}
        </div>
        <Footer>
          <Button type="submit" disabled={save.isPending || (!locked && (!form.host || !form.from))}>
            {save.isPending && <Loader2 className="animate-spin" />}
            {t("setup.email.save")}
            <ArrowRight />
          </Button>
        </Footer>
      </form>
    </Panel>
  );
}

function DoneStep({ onBack }: { onBack: () => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const finish = useMutation({
    mutationFn: () => unwrap(api.POST("/setup/complete")),
    onSuccess: async (st) => {
      qc.setQueryData(setupStatusQuery.queryKey, st);
      await navigate({ to: "/" });
    },
  });
  return (
    <Panel title={t("setup.done.title")} desc={t("setup.done.desc")} icon={<CheckCircle2 className="size-4 text-success" />}>
      {finish.error && (
        <p role="alert" className="px-5 pt-3 text-[13px] text-destructive">
          {errorMessage(finish.error, t("errors.generic"))}
        </p>
      )}
      <Footer onBack={onBack}>
        <Button onClick={() => finish.mutate()} disabled={finish.isPending}>
          {t("setup.done.finish")}
          <ArrowRight />
        </Button>
      </Footer>
    </Panel>
  );
}
