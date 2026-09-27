import { useEffect } from "react";
import { queryOptions, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError, CSRF_COOKIE, CSRF_HEADER, readCookie, type components } from "@kmdn/api-client";
import { api, unwrap } from "@/lib/api";
import { realtime } from "@/lib/realtime";
import type { RepoView } from "@/lib/repos";

export type RevisionView = components["schemas"]["RevisionView"];
export type RevisionFile = components["schemas"]["RevisionFile"];
export type RevisionTreeNode = components["schemas"]["RevisionTreeNode"];
export type RevisionState = components["schemas"]["RevisionState"];

export const revisionByNumberQuery = (repo: RepoView, n: number) =>
  queryOptions({
    queryKey: ["revision", repo.id, n],
    queryFn: () => unwrap(api.GET("/repos/{repo}/revisions/by-number/{number}", { params: { path: { repo: repo.id, number: n } } })),
  });

export function useRevision(repo: RepoView, n: number | undefined) {
  return useQuery({ ...revisionByNumberQuery(repo, n ?? 0), enabled: !!n });
}

export function useRevisions(repo: RepoView | undefined, opts: { state?: string; mine?: boolean } = {}) {
  return useQuery({
    queryKey: ["revisions", repo?.id, opts.state ?? "open", !!opts.mine],
    queryFn: async () =>
      (await unwrap(api.GET("/repos/{repo}/revisions", { params: { path: { repo: repo!.id }, query: { state: opts.state, mine: opts.mine || undefined } } }))).items,
    enabled: !!repo,
  });
}

export function useRevisionFiles(rev: RevisionView | undefined) {
  return useQuery({
    queryKey: ["revision-files", rev?.id],
    queryFn: async () => (await unwrap(api.GET("/revisions/{revision}/files", { params: { path: { revision: rev!.id } } }))).items,
    enabled: !!rev,
  });
}

export function useRevisionTree(rev: RevisionView | undefined) {
  return useQuery({
    queryKey: ["revision-tree", rev?.id],
    queryFn: () => unwrap(api.GET("/revisions/{revision}/tree", { params: { path: { revision: rev!.id } } })),
    enabled: !!rev,
  });
}

export function useRevisionContent(rev: RevisionView | undefined, path: string, enabled = true) {
  return useQuery({
    queryKey: ["revision-content", rev?.id, path],
    queryFn: () => unwrap(api.GET("/revisions/{revision}/files/{path}", { params: { path: { revision: rev!.id, path } } })),
    enabled: enabled && !!rev && !!path,
    retry: false,
  });
}

/** Starts a revision; with `path`, that page is in it from the start. */
export function useCreateRevision(repo: RepoView) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: components["schemas"]["RevisionCreate"]) => unwrap(api.POST("/repos/{repo}/revisions", { params: { path: { repo: repo.id } }, body })),
    onSuccess: (rev) => {
      qc.setQueryData(revisionByNumberQuery(repo, rev.number).queryKey, rev);
      void qc.invalidateQueries({ queryKey: ["revisions", repo.id] });
    },
  });
}

/** Keeps revision queries fresh from realtime events. */
export function useRevisionEvents(repo: RepoView, rev: RevisionView | undefined) {
  const qc = useQueryClient();
  const id = rev?.id;
  const n = rev?.number;
  useEffect(() => {
    if (!id) return;
    return realtime.follow("revision:" + id, (ev) => {
      if (ev.type === "revision") {
        void qc.invalidateQueries({ queryKey: ["revision", repo.id, n] });
        void qc.invalidateQueries({ queryKey: ["revision-files", id] });
        void qc.invalidateQueries({ queryKey: ["revision-tree", id] });
        void qc.invalidateQueries({ queryKey: ["revisions", repo.id] });
      } else if (ev.type === "file_content") {
        void qc.invalidateQueries({ queryKey: ["revision-files", id] });
      }
    });
  }, [qc, repo.id, id, n]);
}

/** A page's title: front-matter title, else its first heading, else its file name. */
export function pageTitle(markdown: string, path: string): string {
  const fm = /^---\r?\n([\s\S]*?)\r?\n---/.exec(markdown)?.[1];
  const t = fm && /^title:\s*["']?(.+?)["']?\s*$/m.exec(fm)?.[1];
  if (t) return t;
  const h = /^#{1,6}\s+(.+?)\s*#*\s*$/m.exec(markdown)?.[1];
  if (h) return h;
  const base = path.split("/").pop() ?? path;
  return base.replace(/\.(md|markdown|mdx)$/i, "");
}

export type UploadResult = components["schemas"]["UploadResult"];

/** Uploads an image into the revision next to page (multipart, so outside openapi-fetch). */
export async function uploadAsset(revisionID: string, page: string, file: File): Promise<UploadResult> {
  const form = new FormData();
  form.append("page", page);
  form.append("file", file, file.name || "image.png");
  const res = await fetch(`/api/v1/revisions/${encodeURIComponent(revisionID)}/assets`, {
    method: "POST",
    body: form,
    credentials: "same-origin",
    headers: { [CSRF_HEADER]: readCookie(document.cookie, CSRF_COOKIE) ?? "" },
  });
  const body = await res.json().catch(() => ({}));
  if (!res.ok) throw new ApiError({ status: res.status, title: res.statusText, code: "upload_failed", ...body });
  return body as UploadResult;
}

/** URL of an image as the revision sees it (its uploads, then the base). */
export function revisionRawUrl(revisionID: string, path: string) {
  return `/api/v1/revisions/${encodeURIComponent(revisionID)}/raw/${path.split("/").map(encodeURIComponent).join("/")}`;
}
