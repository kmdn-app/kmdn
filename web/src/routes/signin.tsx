import { useState, type FormEvent } from "react";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Mail, ArrowLeft, KeyRound, Loader2 } from "lucide-react";
import { AuthLayout } from "@/components/auth/auth-layout";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ForgeIcon } from "@/components/shell/forge-icon";
import { ApiError, api, errorMessage, refreshMe, unwrap, useSetupStatus } from "@/lib/api";
import { cancelled, getPasskey, passkeysSupported } from "@/lib/passkeys";

export const Route = createFileRoute("/signin")({
  validateSearch: (s: Record<string, unknown>): { redirect?: string; oauth_error?: string } => ({
    // Same-origin paths only ("//host" and "/\\host" are other sites to browsers).
    ...(typeof s.redirect === "string" && s.redirect.startsWith("/") && !s.redirect.startsWith("//") && !s.redirect.includes("\\") ? { redirect: s.redirect } : {}),
    ...(typeof s.oauth_error === "string" ? { oauth_error: s.oauth_error } : {}),
  }),
  component: SignIn,
});

function SignIn() {
  const { t } = useTranslation();
  const { data: setup } = useSetupStatus();
  const instance = setup?.instance_name ?? "kmdn";
  const [email, setEmail] = useState("");
  const [sentTo, setSentTo] = useState<{ email: string; minutes: number } | null>(null);

  return (
    <AuthLayout footer={`${instance} · kmdn`}>
      {sentTo ? (
        <CheckEmail email={sentTo.email} minutes={sentTo.minutes} onBack={() => setSentTo(null)} />
      ) : (
        <RequestForm instance={instance} email={email} setEmail={setEmail} onSent={setSentTo} t={t} />
      )}
    </AuthLayout>
  );
}

function RequestForm({
  instance,
  email,
  setEmail,
  onSent,
  t,
}: {
  instance: string;
  email: string;
  setEmail: (v: string) => void;
  onSent: (v: { email: string; minutes: number }) => void;
  t: ReturnType<typeof useTranslation>["t"];
}) {
  const send = useMutation({
    mutationFn: () => unwrap(api.POST("/auth/magic-link", { body: { email } })),
    onSuccess: (r) => onSent({ email: email.trim(), minutes: r.expires_in_minutes }),
  });
  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    send.mutate();
  };
  const err = send.error;
  const { redirect, oauth_error } = Route.useSearch();
  const providers = useQuery({ queryKey: ["oauth-providers"], queryFn: async () => (await unwrap(api.GET("/auth/oauth/providers"))).items });
  return (
    <form onSubmit={onSubmit} noValidate>
      <h1 className="mt-3.5 text-center text-xl font-semibold tracking-tight">{t("signin.title", { instance })}</h1>
      <p className="mt-1.5 mb-6 text-center text-muted-foreground">{t("signin.subtitle")}</p>
      {oauth_error && (
        <p role="alert" className="mb-4 rounded-lg border border-destructive/30 bg-destructive/5 p-3 text-[0.8125rem]">
          {t(`signin.oauthError.${oauth_error}`, { defaultValue: t("signin.oauthError.generic") })}
        </p>
      )}
      <PasskeySignIn redirect={redirect} />
      {(providers.data?.length ?? 0) > 0 && (
        <>
          <div className="grid gap-2">
            {providers.data!.map((p) => (
              <Button key={p.id} asChild variant="outline" className="w-full">
                <a href={`/api/v1/auth/oauth/${p.id}/start?mode=signin&redirect=${encodeURIComponent(redirect ?? "/")}`}>
                  <ForgeIcon kind={p.kind} />
                  {t("signin.withForge", { name: p.display_name })}
                </a>
              </Button>
            ))}
          </div>
          <div className="my-5 flex items-center gap-3 text-xs text-muted-foreground">
            <span className="h-px flex-1 bg-border" />
            {t("signin.or")}
            <span className="h-px flex-1 bg-border" />
          </div>
        </>
      )}
      <div className="mb-4 grid gap-1.5">
        <Label htmlFor="signin-email">{t("signin.email")}</Label>
        <Input
          id="signin-email"
          type="email"
          autoComplete="email"
          autoFocus
          required
          placeholder={t("signin.emailPlaceholder")}
          value={email}
          onChange={(e) => setEmail(e.target.value)}
          aria-invalid={!!err || undefined}
          aria-describedby={err ? "signin-error" : undefined}
        />
        {err && (
          <p id="signin-error" role="alert" className="text-[0.8125rem] text-destructive">
            {err instanceof ApiError && err.status === 429 ? t("signin.rateLimited") : errorMessage(err, t("errors.network"))}
          </p>
        )}
      </div>
      <Button type="submit" className="w-full" disabled={send.isPending || !email.trim()}>
        {send.isPending ? <Loader2 className="animate-spin" /> : <Mail />}
        {send.isPending ? t("signin.sending") : t("signin.sendLink")}
      </Button>
    </form>
  );
}

/** "Sign in with a passkey": the browser lists this site's passkeys, no email needed. */
function PasskeySignIn({ redirect }: { redirect?: string }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [error, setError] = useState<string | null>(null);
  const signIn = useMutation({
    mutationFn: async () => {
      const o = await unwrap(api.POST("/auth/passkey/options"));
      const credential = await getPasskey(o.options.publicKey);
      return unwrap(api.POST("/auth/passkey/verify", { body: { ceremony: o.ceremony, credential } }));
    },
    onSuccess: async () => {
      await refreshMe(qc);
      await navigate({ to: redirect ?? "/" });
    },
    onError: (e) => setError(cancelled(e) ? null : errorMessage(e, t("signin.passkeyFailed"))),
  });
  if (!passkeysSupported()) return null;
  return (
    <div className="mb-5 grid gap-2">
      <Button type="button" variant="outline" className="w-full" disabled={signIn.isPending} onClick={() => (setError(null), signIn.mutate())}>
        {signIn.isPending ? <Loader2 className="animate-spin" /> : <KeyRound />}
        {t("signin.passkey")}
      </Button>
      {error && (
        <p role="alert" className="text-[0.8125rem] text-destructive">
          {error}
        </p>
      )}
    </div>
  );
}

function CheckEmail({ email, minutes, onBack }: { email: string; minutes: number; onBack: () => void }) {
  const { t } = useTranslation();
  const [code, setCode] = useState("");
  const qc = useQueryClient();
  const navigate = useNavigate();
  const { redirect } = Route.useSearch();
  const verify = useMutation({
    mutationFn: () => unwrap(api.POST("/auth/magic-link/verify", { body: { email, code } })),
    onSuccess: async () => {
      await refreshMe(qc);
      await navigate({ to: redirect ?? "/" });
    },
  });
  const resend = useMutation({ mutationFn: () => unwrap(api.POST("/auth/magic-link", { body: { email } })) });
  return (
    <div className="text-center">
      <div className="mx-auto mt-5 grid size-12 place-items-center rounded-full bg-muted">
        <Mail className="size-5.5" />
      </div>
      <h1 className="mt-3.5 text-xl font-semibold tracking-tight">{t("signin.checkTitle")}</h1>
      <p className="mt-1.5 mb-6 text-muted-foreground">{t("signin.checkBody", { email, minutes })}</p>
      <form
        className="grid gap-1.5 text-left"
        onSubmit={(e) => {
          e.preventDefault();
          verify.mutate();
        }}
      >
        <Label htmlFor="signin-code">{t("signin.codeLabel")}</Label>
        <div className="flex gap-2">
          <Input
            id="signin-code"
            inputMode="numeric"
            autoComplete="one-time-code"
            maxLength={7}
            className="font-mono tracking-[0.3em]"
            value={code}
            onChange={(e) => setCode(e.target.value.replace(/[^0-9 ]/g, ""))}
            aria-invalid={!!verify.error || undefined}
          />
          <Button type="submit" disabled={code.replace(/\s/g, "").length !== 6 || verify.isPending}>
            {t("signin.codeSubmit")}
          </Button>
        </div>
        {verify.error && (
          <p role="alert" className="text-[0.8125rem] text-destructive">
            {t("signin.invalid")}
          </p>
        )}
      </form>
      <div className="mt-5 grid gap-2">
        <Button variant="outline" className="w-full" onClick={() => resend.mutate()} disabled={resend.isPending || resend.isSuccess}>
          {t("signin.resend")}
        </Button>
        <Button variant="ghost" className="w-full" onClick={onBack}>
          <ArrowLeft />
          {t("signin.useDifferent")}
        </Button>
      </div>
    </div>
  );
}
