import { createFileRoute, redirect } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Building2, Plus } from "lucide-react";
import { Button } from "@/components/ui/button";
import { orgsInfoQuery, orgsQuery, pickOrg } from "@/lib/orgs";

/** Home: the last (or first) organization, or explain there's none yet. */
export const Route = createFileRoute("/_app/")({
  beforeLoad: async ({ context }) => {
    const org = pickOrg(await context.queryClient.ensureQueryData(orgsQuery));
    if (org) throw redirect({ to: "/$org", params: { org: org.slug } });
  },
  component: NoOrg,
});

function NoOrg() {
  const { t } = useTranslation();
  const info = useQuery(orgsInfoQuery);
  const signup = info.data?.signup_url;
  return (
    <div className="mx-auto flex max-w-[45rem] flex-col items-center px-6 py-24 text-center">
      <div className="grid size-12 place-items-center rounded-full bg-muted">
        <Building2 className="size-5" />
      </div>
      <h1 className="mt-4 text-xl font-semibold tracking-tight">{t("orgs.none")}</h1>
      <p className="mt-1.5 max-w-md text-muted-foreground">{t("orgs.noneHint")}</p>
      {signup && (
        <Button asChild className="mt-5">
          <a href={signup}>
            <Plus />
            {t("orgs.createYours")}
          </a>
        </Button>
      )}
    </div>
  );
}
