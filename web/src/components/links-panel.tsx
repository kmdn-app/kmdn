import { Link } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { ArrowDownLeft, ArrowUpRight, ExternalLink, Image as ImageIcon, TriangleAlert } from "lucide-react";
import { Skeleton } from "@/components/ui/skeleton";
import { api, unwrap } from "@/lib/api";
import type { RepoView } from "@/lib/repos";
import type { RevisionView } from "@/lib/revisions";
import { cn } from "@/lib/utils";

/** The Links tab: where a page links to (broken links first) and what links to it. */
export function LinksPanel({ repo, path, revision }: { repo: RepoView; path: string; revision?: RevisionView }) {
  const { t } = useTranslation();
  const q = useQuery({
    queryKey: ["page-links", repo.id, revision?.id ?? "published", path, revision?.updated_at ?? repo.head_sha],
    queryFn: () =>
      revision
        ? unwrap(api.GET("/revisions/{revision}/links/{path}", { params: { path: { revision: revision.id, path } } }))
        : unwrap(api.GET("/repos/{repo}/links/{path}", { params: { path: { repo: repo.id, path } } })),
    enabled: !!path,
  });
  const search = revision ? { revision: revision.number } : {};
  if (q.isLoading) {
    return (
      <div className="grid gap-2 p-4">
        <Skeleton className="h-4 w-2/3" />
        <Skeleton className="h-4 w-1/2" />
      </div>
    );
  }
  if (!q.data) return <p className="p-4 text-[13px] text-muted-foreground">{t("links.unavailable")}</p>;
  const { outgoing, incoming } = q.data;
  const broken = outgoing.filter((o) => o.broken).length;
  const target = (p: string) => (
    <Link to="/$owner/$repo/$" params={{ owner: repo.owner, repo: repo.name, _splat: p }} search={search} className="truncate hover:underline">
      {p}
    </Link>
  );
  return (
    <div className="grid gap-5 p-4 text-[13px]">
      <section>
        <h3 className="mb-2 flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
          <ArrowUpRight className="size-3.5" />
          {t("links.outgoing", { count: outgoing.length })}
          {broken > 0 && <span className="ml-auto text-destructive">{t("links.broken", { count: broken })}</span>}
        </h3>
        {outgoing.length === 0 && <p className="text-muted-foreground">{t("links.noneOut")}</p>}
        <ul className="grid gap-1">
          {outgoing.map((o, i) => (
            <li key={i} className={cn("flex items-start gap-2 rounded-md px-2 py-1.5", o.broken && "bg-destructive/5")}>
              {o.broken ? (
                <TriangleAlert className="mt-0.5 size-3.5 shrink-0 text-destructive" />
              ) : o.kind === "external" ? (
                <ExternalLink className="mt-0.5 size-3.5 shrink-0 text-muted-foreground" />
              ) : o.image ? (
                <ImageIcon className="mt-0.5 size-3.5 shrink-0 text-muted-foreground" />
              ) : (
                <ArrowUpRight className="mt-0.5 size-3.5 shrink-0 text-muted-foreground" />
              )}
              <div className="min-w-0 flex-1">
                {o.kind === "external" ? (
                  <a href={o.url} target="_blank" rel="noreferrer noopener" className="block truncate hover:underline">
                    {o.url}
                  </a>
                ) : o.broken || !o.to_path || o.image ? (
                  <span className="block truncate font-mono text-[12px]">{o.url}</span>
                ) : (
                  <span className="block truncate">
                    {target(o.to_path)}
                    {o.anchor && <span className="text-muted-foreground">#{o.anchor}</span>}
                  </span>
                )}
                <span className="text-[11.5px] text-muted-foreground">
                  {t("links.line", { line: o.line })}
                  {o.broken && ` · ${t(`links.reasons.${o.broken}`)}`}
                </span>
              </div>
            </li>
          ))}
        </ul>
      </section>
      <section>
        <h3 className="mb-2 flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
          <ArrowDownLeft className="size-3.5" />
          {t("links.incoming", { count: incoming.length })}
        </h3>
        {incoming.length === 0 && <p className="text-muted-foreground">{t("links.noneIn")}</p>}
        <ul className="grid gap-1">
          {incoming.map((l, i) => (
            <li key={i} className="flex items-start gap-2 rounded-md px-2 py-1.5">
              <ArrowDownLeft className="mt-0.5 size-3.5 shrink-0 text-muted-foreground" />
              <div className="min-w-0 flex-1">
                <span className="block truncate">{target(l.from_path)}</span>
                <span className="text-[11.5px] text-muted-foreground">{t("links.line", { line: l.line })}</span>
              </div>
            </li>
          ))}
        </ul>
      </section>
    </div>
  );
}
