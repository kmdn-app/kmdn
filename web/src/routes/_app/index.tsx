import { createFileRoute, redirect } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { Building2 } from "lucide-react";
import { orgsQuery, pickOrg } from "@/lib/orgs";

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
  return (
    <div className="mx-auto flex max-w-[45rem] flex-col items-center px-6 py-24 text-center">
      <div className="grid size-12 place-items-center rounded-full bg-muted">
        <Building2 className="size-5" />
      </div>
      <h1 className="mt-4 text-xl font-semibold tracking-tight">{t("orgs.none")}</h1>
      <p className="mt-1.5 max-w-md text-muted-foreground">{t("orgs.noneHint")}</p>
    </div>
  );
}
