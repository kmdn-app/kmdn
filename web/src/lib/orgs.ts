import { queryOptions, useQuery } from "@tanstack/react-query";
import type { components } from "@kmdn/api-client";
import { api, unwrap } from "@/lib/api";

export type OrgView = components["schemas"]["OrgView"];

export const orgsQuery = queryOptions({
  queryKey: ["orgs"],
  queryFn: async () => (await unwrap(api.GET("/orgs"))).items,
  staleTime: 60_000,
});

/** The mode (single or multi) and whether the caller may create an org. */
export const orgsInfoQuery = queryOptions({
  queryKey: ["orgs", "info"],
  queryFn: async () => {
    const { mode, can_create, org_forges, signup_url } = await unwrap(api.GET("/orgs"));
    return { mode, can_create, org_forges, signup_url };
  },
  staleTime: 60_000,
});

const KEY = "kmdn-org";
let current = "";

/** The org the app works in: the one remembered in this browser, else the first. */
export function pickOrg(orgs: OrgView[]): OrgView | undefined {
  let stored: string | null = null;
  try {
    stored = localStorage.getItem(KEY);
  } catch {
    /* storage unavailable */
  }
  return orgs.find((o) => o.slug === stored) ?? orgs[0];
}

/** Makes slug the current org (set by the signed-in layout before any page loads). */
export function setCurrentOrg(slug: string) {
  current = slug;
  try {
    localStorage.setItem(KEY, slug);
  } catch {
    /* storage unavailable */
  }
}

/** The current org's slug, for API paths under /orgs/{org}. */
export function currentOrg(): string {
  return current;
}

export function useOrgs() {
  return useQuery(orgsQuery);
}

/** The current org, once the list has loaded. */
export function useCurrentOrg(): OrgView | undefined {
  const { data } = useOrgs();
  return data?.find((o) => o.slug === current) ?? (data ? pickOrg(data) : undefined);
}

/** Whether the assistant (and consistency checks) are available in the current org. */
export function useAssistantStatus() {
  const org = currentOrg();
  return useQuery({
    queryKey: ["assistant-status", org],
    queryFn: () => unwrap(api.GET("/orgs/{org}/assistant/status", { params: { path: { org } } })),
    staleTime: 60_000,
  });
}
