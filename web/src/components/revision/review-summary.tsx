import { useEffect } from "react";
import { Link } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { Loader2, RefreshCw, Sparkles, TriangleAlert, Wand2 } from "lucide-react";
import type { components } from "@kmdn/api-client";
import { Time } from "@/components/time";
import { Button } from "@/components/ui/button";
import { api, errorMessage, unwrap } from "@/lib/api";
import { realtime } from "@/lib/realtime";
import type { RepoView } from "@/lib/repos";
import type { RevisionView } from "@/lib/revisions";

type Finding = components["schemas"]["ReviewFinding"];

/** Handled by the Checks section already. */
const LINK_KINDS = new Set(["broken_link", "breaks_inbound"]);

/**
 * The review assistant's card (docs/specs/07-review.md#review-assistant):
 * a summary of what changed and why, the suggested commit, and findings,
 * each with "Fix" (the revision's assistant makes suggestions). Advisory.
 */
export function ReviewSummary({ repo, rev }: { repo: RepoView; rev: RevisionView }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const q = useQuery({
    queryKey: ["review-summary", rev.id, rev.updated_at],
    queryFn: () => unwrap(api.GET("/revisions/{revision}/review-summary", { params: { path: { revision: rev.id } } })),
    refetchInterval: (query) => (query.state.data?.pending ? 3000 : false),
  });
  useEffect(
    () =>
      realtime.follow("revision:" + rev.id, (ev) => {
        if (ev.type === "review_summary") void qc.invalidateQueries({ queryKey: ["review-summary", rev.id] });
      }),
    [qc, rev.id],
  );
  const refresh = useMutation({
    mutationFn: () => unwrap(api.POST("/revisions/{revision}/review-summary", { params: { path: { revision: rev.id } } })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["review-summary", rev.id] }),
    onError: (e) => toast.error(errorMessage(e, t("errors.generic"))),
  });
  const fix = useMutation({
    mutationFn: async (f: Finding) => {
      const th = await unwrap(api.GET("/revisions/{revision}/assistant", { params: { path: { revision: rev.id } } }));
      const where = f.line ? `${f.path}:${f.line}` : f.path;
      const text = t("review.fixPrompt", { message: f.message, where, quote: f.quote ? ` (“${f.quote}”)` : "" });
      return unwrap(api.POST("/assistant/threads/{thread}/messages", { params: { path: { thread: th.thread.id } }, body: { text, context: { path: f.path } } }));
    },
    onSuccess: () => toast.success(t("review.fixAsked")),
    onError: (e) => toast.error(errorMessage(e, t("errors.generic"))),
  });
  const d = q.data;
  if (!d) return null;
  const findings = [...d.checks.filter((c) => !LINK_KINDS.has(c.kind)), ...(d.review?.findings ?? [])];
  if (!d.available && findings.length === 0) return null;
  const canAsk = d.available && rev.state !== "published" && rev.state !== "closed";
  const canFix = canAsk && rev.access.can_edit;
  return (
    <section>
      <div className="mb-2.5 flex items-center gap-2">
        <h2 className="text-[15px] font-semibold">{t("review.title")}</h2>
        {canAsk && (
          <Button size="sm" variant="ghost" className="ml-auto h-7" disabled={d.pending || refresh.isPending} onClick={() => refresh.mutate()}>
            {d.pending ? <Loader2 className="animate-spin" /> : <RefreshCw />}
            {d.review ? t("review.refresh") : t("review.write")}
          </Button>
        )}
      </div>
      <div className="grid gap-3 rounded-xl border p-4 text-[13.5px]">
        {d.pending && !d.review && <p className="flex items-center gap-2 text-muted-foreground"><Loader2 className="size-4 animate-spin" />{t("review.writing")}</p>}
        {d.review && (
          <>
            {d.review.error ? (
              <p className="text-muted-foreground">{d.review.error}</p>
            ) : (
              <div className="flex gap-2.5">
                <Sparkles className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
                <p>{d.review.summary}</p>
              </div>
            )}
            {d.review.commit_title && (
              <div className="rounded-lg bg-muted/50 px-3 py-2">
                <div className="text-[11.5px] font-medium tracking-wide text-muted-foreground uppercase">{t("review.commit")}</div>
                <div className="mt-0.5 font-medium">{d.review.commit_title}</div>
                {d.review.commit_body && <p className="mt-1 text-[12.5px] whitespace-pre-wrap text-muted-foreground">{d.review.commit_body}</p>}
              </div>
            )}
            <p className="text-[11.5px] text-muted-foreground">
              {t("review.by")} · <Time iso={d.review.created_at} />
              {d.review.stale && <span className="text-warning"> · {t("review.stale")}</span>}
            </p>
          </>
        )}
        {!d.review && !d.pending && d.available && <p className="text-muted-foreground">{t("review.none")}</p>}
        {findings.length > 0 && (
          <ul className="grid gap-1.5 border-t pt-3">
            {findings.map((f, i) => (
              <li key={i} className="flex items-start gap-2">
                <TriangleAlert className="mt-0.5 size-4 shrink-0 text-warning" />
                <span className="min-w-0 flex-1">
                  <Link to="/$owner/$repo/$" params={{ owner: repo.owner, repo: repo.name, _splat: f.path }} search={{ revision: rev.number }} className="font-medium hover:underline">
                    {f.path}
                    {f.line ? `:${f.line}` : ""}
                  </Link>{" "}
                  {f.message}
                  {f.quote && <span className="text-muted-foreground"> “{f.quote}”</span>}
                </span>
                {canFix && (
                  <Button size="sm" variant="ghost" className="h-6 px-2 text-[12px]" disabled={fix.isPending} onClick={() => fix.mutate(f)}>
                    <Wand2 />
                    {t("review.fix")}
                  </Button>
                )}
              </li>
            ))}
          </ul>
        )}
      </div>
    </section>
  );
}
