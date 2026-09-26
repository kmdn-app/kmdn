import { createFileRoute } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { GitBranch, Plus } from "lucide-react";
import { AppShell } from "@/components/shell/app-shell";
import { TopBar } from "@/components/shell/top-bar";
import { Button } from "@/components/ui/button";
import { useMe, useSetupStatus } from "@/lib/api";

export const Route = createFileRoute("/_app/")({
  component: Home,
});

function Home() {
  const { t } = useTranslation();
  const { data: me } = useMe();
  const { data: setup } = useSetupStatus();
  return (
    <AppShell>
      {(controls) => (
        <>
          <TopBar controls={controls} title={setup?.instance_name ?? "kmdn"} />
          <div className="min-h-0 flex-1 overflow-auto">
            <div className="mx-auto flex max-w-[720px] flex-col items-center px-6 py-24 text-center">
              <div className="grid size-12 place-items-center rounded-full bg-muted">
                <GitBranch className="size-5" />
              </div>
              <h1 className="mt-4 text-xl font-semibold tracking-tight">{t("home.noRepos")}</h1>
              <p className="mt-1.5 max-w-md text-muted-foreground">{me?.is_instance_admin ? t("home.noReposAdmin") : t("home.noReposMember")}</p>
              {me?.is_instance_admin && (
                <Button className="mt-6" disabled>
                  <Plus />
                  {t("home.connect")}
                </Button>
              )}
            </div>
          </div>
        </>
      )}
    </AppShell>
  );
}
