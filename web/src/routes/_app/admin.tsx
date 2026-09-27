import { createFileRoute, redirect } from "@tanstack/react-router";
import { currentOrg, orgsQuery, pickOrg } from "@/lib/orgs";

/** /admin from before orgs (and the forge callbacks): the current org's console. */
export const Route = createFileRoute("/_app/admin")({
  beforeLoad: async ({ context, location }) => {
    const org = currentOrg() || pickOrg(await context.queryClient.ensureQueryData(orgsQuery))?.slug;
    if (!org) throw redirect({ to: "/" });
    throw redirect({ href: `/${org}/admin${location.searchStr}`, replace: true });
  },
});
