import { Outlet, createFileRoute, notFound, redirect } from "@tanstack/react-router";
import { NotFoundPage } from "@/components/not-found";
import { currentOrg, orgsQuery, setCurrentOrg } from "@/lib/orgs";

/**
 * An organization's area: /{org}/… (docs/specs/16-organizations.md#urls-and-the-request-context).
 * A first segment that isn't one of the person's orgs is an address from
 * before orgs (/{owner}/{repo}/…): it opens in the current org.
 */
export const Route = createFileRoute("/_app/$org")({
  beforeLoad: async ({ context, params, location }) => {
    const orgs = await context.queryClient.ensureQueryData(orgsQuery);
    const org = orgs.find((o) => o.slug === params.org);
    if (!org) {
      const fallback = currentOrg() || orgs[0]?.slug;
      const segments = location.pathname.split("/").filter(Boolean);
      if (fallback && segments.length >= 2 && segments[0] !== fallback) {
        throw redirect({ href: `/${fallback}${location.pathname}${location.searchStr}${location.hash ? `#${location.hash}` : ""}`, replace: true });
      }
      throw notFound();
    }
    setCurrentOrg(org.slug);
    return { org };
  },
  notFoundComponent: NotFoundPage,
  component: Outlet,
});
