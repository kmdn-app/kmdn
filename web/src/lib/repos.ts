import { keepPreviousData, queryOptions, useQuery } from "@tanstack/react-query";
import type { components } from "@kmdn/api-client";
import { api, unwrap } from "@/lib/api";
import { currentOrg } from "@/lib/orgs";

export type RepoView = components["schemas"]["RepoView"];
export type TreeNode = components["schemas"]["TreeNode"];
export type RepoFile = components["schemas"]["RepoFile"];
export type Commit = components["schemas"]["Commit"];
export type BlameLine = components["schemas"]["BlameLine"];
export type SearchHit = components["schemas"]["SearchHit"];
export type Member = components["schemas"]["Member"];
export type Role = components["schemas"]["Role"];

/** The current org's repositories (invalidate with the ["repos"] prefix). */
export const reposQuery = () => {
  const org = currentOrg();
  return queryOptions({
    queryKey: ["repos", org],
    queryFn: async () => (await unwrap(api.GET("/orgs/{org}/repos", { params: { path: { org } } }))).items,
  });
};

export function useRepos() {
  return useQuery(reposQuery());
}

export const repoBySlugQuery = (org: string, owner: string, name: string) => {
  return queryOptions({
    queryKey: ["repo", org, owner, name],
    queryFn: () => unwrap(api.GET("/orgs/{org}/repos/by-slug/{owner}/{name}", { params: { path: { org, owner, name } } })),
    // Poll while the first sync runs.
    refetchInterval: (q) => (q.state.data?.health === "pending" ? 1500 : false),
  });
};

export function useTree(repo: RepoView | undefined) {
  return useQuery({
    queryKey: ["tree", repo?.id, repo?.head_sha],
    queryFn: () => unwrap(api.GET("/repos/{repo}/tree", { params: { path: { repo: repo!.id } } })),
    enabled: !!repo?.head_sha,
    staleTime: Infinity,
  });
}

export function useFile(repo: RepoView | undefined, path: string, sha?: string) {
  return useQuery({
    queryKey: ["file", repo?.id, path, sha ?? repo?.head_sha],
    queryFn: () => unwrap(api.GET("/repos/{repo}/files/{path}", { params: { path: { repo: repo!.id, path }, query: sha ? { sha } : {} } })),
    enabled: !!repo?.head_sha && !!path,
    staleTime: sha ? Infinity : 30_000,
    retry: false,
  });
}

export function useHistory(repo: RepoView | undefined, path: string) {
  return useQuery({
    queryKey: ["history", repo?.id, path, repo?.head_sha],
    queryFn: async () => (await unwrap(api.GET("/repos/{repo}/history/{path}", { params: { path: { repo: repo!.id, path } } }))).items,
    enabled: !!repo?.head_sha && !!path,
  });
}

export function useBlame(repo: RepoView | undefined, path: string, enabled: boolean) {
  return useQuery({
    queryKey: ["blame", repo?.id, path, repo?.head_sha],
    queryFn: async () => (await unwrap(api.GET("/repos/{repo}/blame/{path}", { params: { path: { repo: repo!.id, path } } }))).items,
    enabled: enabled && !!repo?.head_sha && !!path,
  });
}

export function useActivity(repo: RepoView | undefined) {
  return useQuery({
    queryKey: ["activity", repo?.id, repo?.head_sha],
    queryFn: async () => (await unwrap(api.GET("/repos/{repo}/activity", { params: { path: { repo: repo!.id } } }))).items,
    enabled: !!repo?.head_sha,
  });
}

export function useSearch(repo: RepoView | undefined, q: string) {
  return useQuery({
    queryKey: ["search", repo?.id, q],
    queryFn: async () => (await unwrap(api.GET("/repos/{repo}/search", { params: { path: { repo: repo!.id }, query: { q } } }))).items,
    enabled: !!repo && q.trim().length > 1,
    placeholderData: keepPreviousData,
    staleTime: 10_000,
  });
}

export function rawUrl(repo: RepoView, path: string, sha?: string) {
  const enc = path.split("/").map(encodeURIComponent).join("/");
  return `/api/v1/repos/${repo.id}/raw/${enc}${sha ? `?sha=${sha}` : ""}`;
}

/** App route for a file in a repo. */
export function fileHref(repo: RepoView, path: string) {
  return `/${repo.org_slug}/${repo.owner}/${repo.name}/${path}`;
}

const RANK: Record<string, number> = { "": 0, viewer: 1, contributor: 2, maintainer: 3, admin: 4 };
export function atLeast(role: Role | undefined, min: Exclude<Role, "">) {
  return RANK[role ?? ""]! >= RANK[min]!;
}

/** A commit's page on the forge (null for plain git). */
export function commitURL(repo: RepoView, sha: string): string | null {
  if (!repo.web_url) return null;
  if (repo.forge_kind === "github") return `${repo.web_url}/commit/${sha}`;
  if (repo.forge_kind === "gitlab") return `${repo.web_url}/-/commit/${sha}`;
  return null;
}
