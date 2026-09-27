import { useState } from "react";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Eye, FilePen, Loader2, Lock, MessageSquare } from "lucide-react";
import { AuthLayout } from "@/components/auth/auth-layout";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api, errorMessage, refreshMe, unwrap } from "@/lib/api";

export const Route = createFileRoute("/invite/$token")({
  component: Invite,
});

function Invite() {
  const { t } = useTranslation();
  const { token } = Route.useParams();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [name, setName] = useState("");
  const inv = useQuery({ queryKey: ["invite", token], queryFn: () => unwrap(api.GET("/invites/{token}", { params: { path: { token } } })), retry: false });
  const accept = useMutation({
    mutationFn: () => unwrap(api.POST("/invites/{token}/accept", { params: { path: { token } }, body: { name } })),
    onSuccess: async () => {
      await refreshMe(qc);
      await navigate({ to: "/" });
    },
  });
  const d = inv.data;
  return (
    <AuthLayout footer={d?.instance_name}>
      {inv.isLoading && <Loader2 className="mx-auto mt-6 animate-spin" />}
      {inv.error && <p className="mt-5 text-center">{errorMessage(inv.error, t("invite.invalid"))}</p>}
      {d && (
        <div className="mt-4">
          <h1 className="text-center text-xl font-semibold tracking-tight text-balance">
            {d.repo_name ? t("invite.titleRepo", { inviter: d.inviter_name, repo: d.repo_name }) : t("invite.title", { inviter: d.inviter_name, instance: d.instance_name })}
          </h1>
          {d.role && <p className="mt-1.5 text-center text-muted-foreground">{t("invite.asRole", { role: t(`roles.${d.role}`) })}</p>}
          {d.role && (
            <div className="mt-5 grid gap-2 rounded-lg border p-3.5 text-[13.5px]">
              <div className="flex items-center gap-2">
                <Eye className="size-4 text-muted-foreground" />
                {t("invite.canRead")}
              </div>
              {d.role !== "viewer" && (
                <div className="flex items-center gap-2">
                  <FilePen className="size-4 text-muted-foreground" />
                  {t("invite.canEdit")}
                </div>
              )}
              <div className="flex items-center gap-2">
                <MessageSquare className="size-4 text-muted-foreground" />
                {t("invite.canComment")}
              </div>
              {d.role !== "maintainer" && d.role !== "admin" && (
                <div className="flex items-center gap-2 text-muted-foreground">
                  <Lock className="size-4" />
                  {t("invite.needsApproval")}
                </div>
              )}
            </div>
          )}
          <form
            className="mt-5 grid gap-3"
            onSubmit={(e) => {
              e.preventDefault();
              accept.mutate();
            }}
          >
            <div className="rounded-lg border px-3 py-2 text-[13.5px]">
              <div className="text-xs text-muted-foreground">{t("invite.signingInAs")}</div>
              <div className="font-medium">{d.email}</div>
            </div>
            {!d.has_account && (
              <div className="grid gap-1.5">
                <Label htmlFor="inv-name">{t("invite.yourName")}</Label>
                <Input id="inv-name" autoComplete="name" value={name} onChange={(e) => setName(e.target.value)} autoFocus />
              </div>
            )}
            {accept.error && <p className="text-[13px] text-destructive">{errorMessage(accept.error, t("errors.generic"))}</p>}
            <Button type="submit" className="w-full" disabled={accept.isPending || (!d.has_account && !name.trim())}>
              {accept.isPending && <Loader2 className="animate-spin" />}
              {t("invite.accept")}
            </Button>
          </form>
          <p className="mt-4 text-center text-xs text-muted-foreground">{t("invite.expires", { date: new Date(d.expires_at).toLocaleDateString() })}</p>
        </div>
      )}
    </AuthLayout>
  );
}
