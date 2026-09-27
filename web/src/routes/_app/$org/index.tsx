import { createFileRoute, Link, redirect } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { GitBranch, Plus } from "lucide-react";
import { AppShell } from "@/components/shell/app-shell";
import { TopBar } from "@/components/shell/top-bar";
import { Button } from "@/components/ui/button";
import { useMe, useSetupStatus } from "@/lib/api";
import { reposQuery } from "@/lib/repos";
import { lastRepo } from "@/lib/last-repo";
import { currentOrg, useCurrentOrg } from "@/lib/orgs";


/** Home: open the last (or first) repository, or explain what's missing. */
export const Route = createFileRoute("/_app/$org/")({
  beforeLoad: async ({ context }) => {
    const repos = await context.queryClient.ensureQueryData(reposQuery());
    const last = lastRepo();
    const pick = repos.find((r) => r.id === last) ?? repos[0];
    if (pick) throw redirect({ to: "/$org/$owner/$repo", params: { org: pick.org_slug, owner: pick.owner, repo: pick.name } });
  },
  component: Empty,
});

function Empty() {
  const { t } = useTranslation();
  const { data: me } = useMe();
  const { data: setup } = useSetupStatus();
  const org = useCurrentOrg();
  const canAdmin = !!me?.is_instance_admin || org?.role === "admin" || org?.role === "owner";
  return (
    <AppShell>
      {(controls) => (
        <>
          <TopBar controls={controls} title={org?.name ?? setup?.instance_name ?? "kmdn"} />
          <div className="min-h-0 flex-1 overflow-auto">
            <div className="mx-auto flex max-w-[45rem] flex-col items-center px-6 py-24 text-center">
              <div className="grid size-12 place-items-center rounded-full bg-muted">
                <GitBranch className="size-5" />
              </div>
              <h1 className="mt-4 text-xl font-semibold tracking-tight">{t("home.noRepos")}</h1>
              <p className="mt-1.5 max-w-md text-muted-foreground">{canAdmin ? t("home.noReposAdmin") : t("home.noReposMember")}</p>
              {canAdmin && (
                <Button asChild className="mt-6">
                  <Link to="/$org/admin" params={{ org: currentOrg() }} search={{ section: "repositories" }}>
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
