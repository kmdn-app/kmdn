import { createFileRoute, Link, redirect } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { GitBranch, Plus } from "lucide-react";
import { AppShell } from "@/components/shell/app-shell";
import { TopBar } from "@/components/shell/top-bar";
import { Button } from "@/components/ui/button";
import { useMe, useSetupStatus } from "@/lib/api";
import { reposQuery } from "@/lib/repos";
import { lastRepo } from "@/lib/last-repo";


/** Home: open the last (or first) repository, or explain what's missing. */
export const Route = createFileRoute("/_app/")({
  beforeLoad: async ({ context }) => {
    const repos = await context.queryClient.ensureQueryData(reposQuery);
    const last = lastRepo();
    const pick = repos.find((r) => r.id === last) ?? repos[0];
    if (pick) throw redirect({ to: "/$owner/$repo", params: { owner: pick.owner, repo: pick.name } });
  },
  component: Empty,
});

function Empty() {
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
                <Button asChild className="mt-6">
                  <Link to="/admin" search={{ section: "repositories" }}>
                    <Plus />
                    {t("home.connect")}
                  </Link>
                </Button>
              )}
            </div>
          </div>
        </>
      )}
    </AppShell>
  );
}
