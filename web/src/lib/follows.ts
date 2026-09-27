import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { components } from "@kmdn/api-client";
import { api, unwrap } from "@/lib/api";

export type FollowState = components["schemas"]["FollowState"];
export type PageRead = components["schemas"]["PageRead"];

export function useFollowState(repoID: string, path: string, enabled = true) {
  return useQuery({
    queryKey: ["follow", repoID, path],
    queryFn: () => unwrap(api.GET("/repos/{repo}/follows", { params: { path: { repo: repoID }, query: { path } } })),
    enabled,
  });
}

export function useToggleFollow(repoID: string, path: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (on: boolean) =>
      on
        ? unwrap(api.PUT("/repos/{repo}/follows", { params: { path: { repo: repoID }, query: { path } } }))
        : unwrap(api.DELETE("/repos/{repo}/follows", { params: { path: { repo: repoID }, query: { path } } })),
    onSuccess: (st) => qc.setQueryData(["follow", repoID, path], st),
  });
}

/**
 * Records that the page was read at sha (its latest commit) and returns the
 * previous read, once per page and version.
 */
export function usePageRead(repoID: string, path: string, sha: string | undefined): PageRead | null | undefined {
  const [prev, setPrev] = useState<{ key: string; read: PageRead | null } | null>(null);
  const key = `${repoID}:${path}:${sha}`;
  useEffect(() => {
    if (!sha) return;
    let live = true;
    unwrap(api.POST("/repos/{repo}/reads", { params: { path: { repo: repoID } }, body: { path, sha } }))
      .then((r) => live && setPrev({ key, read: r.previous ?? null }))
      .catch(() => live && setPrev({ key, read: null }));
    return () => {
      live = false;
    };
  }, [repoID, path, sha, key]);
  return prev?.key === key ? prev.read : undefined;
}

export function useFollowUpdates(repoID: string) {
  return useQuery({
    queryKey: ["follow-updates", repoID],
    queryFn: async () => (await unwrap(api.GET("/repos/{repo}/follows/updates", { params: { path: { repo: repoID } } }))).items,
  });
}
