import { getRouteApi } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { repoBySlugQuery } from "@/lib/repos";

const route = getRouteApi("/_app/$org/$owner/$repo");

/** The repository of the current /$owner/$repo route. */
export function useRepo() {
  const { org, owner, repo } = route.useParams();
  const q = useQuery(repoBySlugQuery(org, owner, repo));
  return q.data!;
}
