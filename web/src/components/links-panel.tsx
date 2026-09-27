import { Link, useNavigate } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { ArrowDownLeft, ArrowUpRight, ExternalLink, Image as ImageIcon, TriangleAlert, Waypoints } from "lucide-react";
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
      <LocalGraph repo={repo} path={path} outgoing={outgoing} incoming={incoming} search={search} />
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

type Neighbor = { path: string; out: boolean; in: boolean; broken: boolean };

/** The page and its direct neighbours: linked to (right), linking here (left), both (top/bottom). */
function LocalGraph({
  repo,
  path,
  outgoing,
  incoming,
  search,
}: {
  repo: RepoView;
  path: string;
  outgoing: { to_path?: string; kind: string; image?: boolean; broken?: string }[];
  incoming: { from_path: string }[];
  search: { revision?: number };
}) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const by = new Map<string, Neighbor>();
  for (const o of outgoing) {
    if (!o.to_path || o.image || o.kind === "external" || o.kind === "anchor" || o.to_path === path) continue;
    const n = by.get(o.to_path) ?? { path: o.to_path, out: false, in: false, broken: false };
    n.out = true;
    n.broken ||= o.broken === "missing_page";
    by.set(o.to_path, n);
  }
  for (const i of incoming) {
    if (i.from_path === path) continue;
    const n = by.get(i.from_path) ?? { path: i.from_path, out: false, in: false, broken: false };
    n.in = true;
    by.set(i.from_path, n);
  }
  const all = [...by.values()];
  if (!all.length) return null;
  const shown = all.slice(0, 14);
  const W = 300;
  const H = 190;
  const cx = W / 2;
  const cy = H / 2;
  // Outgoing on the right, incoming on the left, both across the middle.
  const groups = { out: shown.filter((n) => n.out && !n.in), in: shown.filter((n) => n.in && !n.out), both: shown.filter((n) => n.in && n.out) };
  const place = (list: Neighbor[], from: number, to: number) =>
    list.map((n, i) => {
      const a = list.length === 1 ? (from + to) / 2 : from + ((to - from) * i) / (list.length - 1);
      return { n, x: cx + Math.cos(a) * 112, y: cy + Math.sin(a) * 70 };
    });
  const pts = [...place(groups.out, -Math.PI / 2.6, Math.PI / 2.6), ...place(groups.in, Math.PI - Math.PI / 2.6, Math.PI + Math.PI / 2.6), ...place(groups.both, -Math.PI / 2 - 0.35, -Math.PI / 2 + 0.35)];
  const label = (p: string) => {
    const b = p.split("/").pop()!.replace(/\.mdx?$/, "");
    return b.length > 16 ? b.slice(0, 15) + "…" : b;
  };
  const go = (p: string) => void navigate({ to: "/$owner/$repo/$", params: { owner: repo.owner, repo: repo.name, _splat: p }, search });
  return (
    <section>
      <h3 className="mb-2 flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
        <Waypoints className="size-3.5" />
        {t("graph.local")}
        <Link to="/$owner/$repo/graph" params={{ owner: repo.owner, repo: repo.name }} search={search} className="ml-auto hover:text-foreground">
          {t("graph.openGraph")}
        </Link>
      </h3>
      <svg viewBox={`0 0 ${W} ${H}`} className="w-full rounded-lg border bg-muted/30" role="img" aria-label={t("graph.local")}>
        {pts.map(({ n, x, y }) => (
          <line key={"l" + n.path} x1={cx} y1={cy} x2={x} y2={y} strokeWidth={1.2} strokeDasharray={n.broken ? "4 3" : undefined} className={n.broken ? "stroke-destructive" : n.out ? "stroke-primary/50" : "stroke-muted-foreground/40"} />
        ))}
        <circle cx={cx} cy={cy} r={8} className="fill-primary" />
        {pts.map(({ n, x, y }) => (
          <g key={n.path} className={cn(!n.broken && "cursor-pointer")} onClick={() => !n.broken && go(n.path)}>
            <title>{n.path}</title>
            <circle cx={x} cy={y} r={5} strokeWidth={1.2} strokeDasharray={n.broken ? "2 2" : undefined} className={n.broken ? "fill-background stroke-destructive" : "fill-background stroke-foreground/60"} />
            <text x={x} y={y < cy - 20 ? y - 9 : y + 15} textAnchor="middle" className={cn("text-[9px]", n.broken ? "fill-destructive" : "fill-muted-foreground")}>
              {label(n.path)}
            </text>
          </g>
        ))}
      </svg>
      {all.length > shown.length && <p className="mt-1 text-[11.5px] text-muted-foreground">{t("links.more", { count: all.length - shown.length })}</p>}
    </section>
  );
}
