import { Outlet, createFileRoute, notFound } from "@tanstack/react-router";
import { ApiError } from "@/lib/api";
import { repoBySlugQuery } from "@/lib/repos";
import { NotFoundPage } from "@/components/not-found";
import { rememberRepo } from "@/lib/last-repo";

export const Route = createFileRoute("/_app/$org/$owner/$repo")({
  loader: async ({ context, params }) => {
    try {
      const repo = await context.queryClient.ensureQueryData(repoBySlugQuery(params.org, params.owner, params.repo));
      rememberRepo(repo.id);
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) throw notFound();
      throw e;
    }
  },
  notFoundComponent: NotFoundPage,
  component: Outlet,
});
