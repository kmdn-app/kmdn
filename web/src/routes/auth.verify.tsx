import { useEffect, useRef, useState } from "react";
import { Link, createFileRoute, useNavigate } from "@tanstack/react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Loader2 } from "lucide-react";
import { AuthLayout } from "@/components/auth/auth-layout";
import { Button } from "@/components/ui/button";
import { api, refreshMe, unwrap } from "@/lib/api";

export const Route = createFileRoute("/auth/verify")({
  validateSearch: (s: Record<string, unknown>): { token?: string } => (typeof s.token === "string" ? { token: s.token } : {}),
  component: Verify,
});

function Verify() {
  const { t } = useTranslation();
  // The token leaves the address bar once used: keep the one the page opened with.
  const [token] = useState(Route.useSearch().token);
  const qc = useQueryClient();
  const navigate = useNavigate();
  const started = useRef(false);
  const verify = useMutation({
    mutationFn: (tok: string) => unwrap(api.POST("/auth/magic-link/verify", { body: { token: tok } })),
    onSuccess: async () => {
      await refreshMe(qc);
      await navigate({ to: "/", replace: true });
    },
  });
  useEffect(() => {
    // Strip the token from the address bar and history right away.
    window.history.replaceState(null, "", "/auth/verify");
    if (token && !started.current) {
      started.current = true;
      verify.mutate(token);
    }
  }, [token, verify]);

  const failed = !token || verify.isError;
  return (
    <AuthLayout>
      <div className="mt-5 text-center" role="status">
        {failed ? (
          <>
            <p className="font-medium">{t("verify.failed")}</p>
            <Button asChild variant="outline" className="mt-5 w-full">
              <Link to="/signin">{t("verify.back")}</Link>
            </Button>
          </>
        ) : (
          <p className="flex items-center justify-center gap-2 text-muted-foreground">
            <Loader2 className="size-4 animate-spin" />
            {t("verify.signingIn")}
          </p>
        )}
      </div>
    </AuthLayout>
  );
}
