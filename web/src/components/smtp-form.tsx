import { useState, type ReactNode } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { AlertCircle, CheckCircle2, Loader2, Send } from "lucide-react";
import type { components } from "@kmdn/api-client";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Separator } from "@/components/ui/separator";
import { api, errorMessage, setupStatusQuery, unwrap } from "@/lib/api";
import { cn } from "@/lib/utils";

type SMTPInput = components["schemas"]["SMTPInput"];

function Field({ id, label, help, children }: { id: string; label: string; help?: string; children: ReactNode }) {
  return (
    <div className="grid gap-1.5">
      <Label htmlFor={id}>{label}</Label>
      {children}
      {help && <p className="text-[0.78125rem] text-muted-foreground">{help}</p>}
    </div>
  );
}

/** SMTP settings with "send a test" of unsaved values. Used by setup and the admin console. */
export function SmtpForm({ saved, defaultTestTo, onDone, submitLabel }: { saved: components["schemas"]["SMTPSettings"]; defaultTestTo: string; onDone: () => void; submitLabel: string }) {
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
      <form
        onSubmit={(e) => {
          e.preventDefault();
          save.mutate();
        }}
      >
        <fieldset disabled={locked} className="grid gap-4 px-5 pt-4">
          {locked && <p className="rounded-lg border bg-muted p-3 text-[0.84375rem]">{t("setup.email.locked")}</p>}
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
            <Input id="smtp-test-to" type="email" className="min-w-[12.5rem] flex-1" value={testTo} onChange={(e) => setTestTo(e.target.value)} />
            <Button type="button" variant="outline" onClick={() => test.mutate()} disabled={test.isPending || (!locked && !form.host)}>
              {test.isPending ? <Loader2 className="animate-spin" /> : <Send />}
              {t("setup.email.test")}
            </Button>
          </div>
          {test.data && (
            <p
              role="status"
              className={cn(
                "mt-3 flex items-start gap-2 rounded-lg border p-3 text-[0.84375rem]",
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
            <p role="alert" className="mt-3 text-[0.8125rem] text-destructive">
              {errorMessage(save.error, t("errors.generic"))}
            </p>
          )}
        </div>
        <div className="mt-4 flex items-center justify-end gap-2 border-t px-5 py-3">
          <Button type="submit" disabled={save.isPending || (!locked && (!form.host || !form.from))}>
            {save.isPending && <Loader2 className="animate-spin" />}
            {submitLabel}
          </Button>
        </div>
      </form>
  );
}

