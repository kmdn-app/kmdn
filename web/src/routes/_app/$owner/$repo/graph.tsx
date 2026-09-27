import { useCallback, useMemo, useState } from "react";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { FileText, Search, X } from "lucide-react";
import { AppShell } from "@/components/shell/app-shell";
import { TopBar } from "@/components/shell/top-bar";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { FOLDER_COLORS, GraphCanvas, type GEdge, type GNode } from "@/components/graph/graph-canvas";
import { api, unwrap } from "@/lib/api";
import { useRevision, useRevisions } from "@/lib/revisions";
import { useRepo } from "@/lib/use-repo";

type Search = { revision?: number };

export const Route = createFileRoute("/_app/$owner/$repo/graph")({
  validateSearch: (s: Record<string, unknown>): Search => (Number.isInteger(Number(s.revision)) && Number(s.revision) > 0 ? { revision: Number(s.revision) } : {}),
  component: GraphPage,
});

const ALL = "__all";

function GraphPage() {
  const { t } = useTranslation();
  const repo = useRepo();
  const { revision } = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });
  const open = useRevisions(repo, { mine: true });
  const current = useRevision(repo, revision);
  const scopeRev = revision ? current.data : undefined;
  const scope = scopeRev ? `revision:${scopeRev.id}` : "published";
  const graph = useQuery({
    queryKey: ["graph", repo.id, scope],
    queryFn: () => unwrap(api.GET("/repos/{repo}/graph", { params: { path: { repo: repo.id }, query: { scope } } })),
    enabled: !revision || !!scopeRev,
  });
  const [folder, setFolder] = useState(ALL);
  const [q, setQ] = useState("");
  const [selected, setSelected] = useState<string | null>(null);
  const [showDups, setShowDups] = useState(false);
  const folders = useMemo(() => [...new Set((graph.data?.nodes ?? []).map((n) => n.folder))].sort(), [graph.data]);
  const colorOf = useCallback((f: string) => FOLDER_COLORS[Math.max(0, folders.indexOf(f)) % FOLDER_COLORS.length]!, [folders]);
  const { nodes, edges } = useMemo(() => {
    const all = graph.data?.nodes ?? [];
    const keep = folder === ALL ? all : all.filter((n) => n.folder === folder);
    const paths = new Set(keep.map((n) => n.path));
    const links: GEdge[] = (graph.data?.edges ?? []).filter((e) => paths.has(e.from) && (e.broken || paths.has(e.to)));
    const dups: GEdge[] = showDups ? (graph.data?.duplicates ?? []).filter((d) => paths.has(d.a) && paths.has(d.b)).map((d) => ({ from: d.a, to: d.b, count: 1, duplicate: true })) : [];
    return { nodes: keep, edges: [...links, ...dups] };
  }, [graph.data, folder, showDups]);
  const match = useMemo(() => {
    const s = q.trim().toLowerCase();
    return s ? (n: GNode) => n.title.toLowerCase().includes(s) || n.path.toLowerCase().includes(s) : null;
  }, [q]);
  const orphans = nodes.filter((n) => n.orphan);
  const broken = edges.filter((e) => e.broken).length;
  const dupCount = edges.filter((e) => e.duplicate).length;
  const hasDups = (graph.data?.duplicates.length ?? 0) > 0;
  const node = selected ? nodes.find((n) => n.path === selected) : undefined;
  const pageLink = (p: string) => ({ to: "/$owner/$repo/$" as const, params: { owner: repo.owner, repo: repo.name, _splat: p }, search: scopeRev ? { revision: scopeRev.number } : {} });

  return (
    <AppShell repo={repo}>
      {(controls) => (
        <>
          <TopBar
            controls={controls}
            title={t("graph.title")}
            actions={
              <>
                <div className="relative @max-2xl:hidden">
                  <Search className="pointer-events-none absolute top-1/2 left-2 size-3.5 -translate-y-1/2 text-muted-foreground" />
                  <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder={t("graph.search")} className="h-8 w-44 pl-7 text-[13px]" aria-label={t("graph.search")} />
                </div>
                <Select value={folder} onValueChange={setFolder}>
                  <SelectTrigger size="sm" className="w-36" aria-label={t("graph.folder")}>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value={ALL}>{t("graph.allFolders")}</SelectItem>
                    {folders.map((f) => (
                      <SelectItem key={f || "/"} value={f}>
                        <span className="size-2 rounded-full" style={{ background: colorOf(f) }} />
                        {f || t("graph.root")}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                {((open.data?.length ?? 0) > 0 || scopeRev) && (
                  <Select value={revision ? String(revision) : "published"} onValueChange={(v) => void navigate({ search: v === "published" ? {} : { revision: Number(v) }, replace: true })}>
                    <SelectTrigger size="sm" className="w-44" aria-label={t("graph.scope")}>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="published">{t("shell.published")}</SelectItem>
                      {scopeRev && !open.data?.some((r) => r.id === scopeRev.id) && (
                        <SelectItem value={String(scopeRev.number)}>
                          #{scopeRev.number} {scopeRev.title}
                        </SelectItem>
                      )}
                      {(open.data ?? []).map((r) => (
                        <SelectItem key={r.id} value={String(r.number)}>
                          #{r.number} {r.title}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                )}
              </>
            }
          />
          <div className="relative min-h-0 flex-1 overflow-hidden">
            {graph.isLoading && <Skeleton className="absolute inset-6" />}
            {graph.data && nodes.length === 0 && <p className="p-8 text-muted-foreground">{t("graph.empty")}</p>}
            {graph.data && nodes.length > 0 && <GraphCanvas nodes={nodes} edges={edges} colorOf={colorOf} match={match} selected={selected} onSelect={setSelected} cacheKey={`kmdn-graph:${repo.id}:${scope}:${folder}`} />}
            {graph.data && (
              <div className="pointer-events-none absolute top-3 left-3 grid max-w-[260px] gap-2">
                <div className="pointer-events-auto rounded-lg border bg-background/90 px-3 py-2 text-[12px] text-muted-foreground shadow-xs backdrop-blur">
                  {t("graph.stats", { pages: nodes.length, links: edges.length - broken - dupCount })}
                  {broken > 0 && <span className="text-destructive"> · {t("graph.broken", { count: broken })}</span>}
                  {hasDups && (
                    <label className="mt-1.5 flex cursor-pointer items-center gap-2 text-foreground">
                      <Switch checked={showDups} onCheckedChange={setShowDups} className="scale-90" />
                      {t("graph.duplicates")}
                    </label>
                  )}
                </div>
                {orphans.length > 0 && (
                  <details className="pointer-events-auto rounded-lg border bg-background/90 px-3 py-2 text-[12.5px] shadow-xs backdrop-blur">
                    <summary className="cursor-pointer font-medium">{t("graph.orphans", { count: orphans.length })}</summary>
                    <ul className="mt-1.5 grid max-h-56 gap-1 overflow-auto">
                      {orphans.map((o) => (
                        <li key={o.path}>
                          <button type="button" className="truncate text-left text-muted-foreground hover:text-foreground" onClick={() => setSelected(o.path)}>
                            {o.path}
                          </button>
                        </li>
                      ))}
                    </ul>
                  </details>
                )}
              </div>
            )}
            {node && (
              <div className="absolute right-3 bottom-3 w-[280px] rounded-xl border bg-background p-4 shadow-md">
                <div className="flex items-start gap-2">
                  <span className="mt-1.5 size-2.5 shrink-0 rounded-full" style={{ background: colorOf(node.folder) }} />
                  <div className="min-w-0 flex-1">
                    <div className="truncate text-sm font-semibold">{node.title}</div>
                    <div className="truncate font-mono text-[11.5px] text-muted-foreground">{node.path}</div>
                  </div>
                  <button type="button" className="text-muted-foreground hover:text-foreground" onClick={() => setSelected(null)} aria-label={t("common.close")}>
                    <X className="size-4" />
                  </button>
                </div>
                <div className="mt-2 text-[12.5px] text-muted-foreground">
                  {t("graph.inOut", { in: node.in, out: node.out })}
                  {node.orphan && ` · ${t("graph.orphan")}`}
                  {node.changed && ` · ${t("graph.changed")}`}
                </div>
                {showDups &&
                  (graph.data?.duplicates ?? [])
                    .filter((d) => d.a === node.path || d.b === node.path)
                    .map((d) => (
                      <div key={d.a + d.b} className="mt-1 truncate text-[12.5px] text-warning">
                        {t("graph.duplicateOf", { page: d.a === node.path ? d.b : d.a })}
                      </div>
                    ))}
                <Button asChild size="sm" className="mt-3 w-full">
                  <Link {...pageLink(node.path)}>
                    <FileText />
                    {t("graph.open")}
                  </Link>
                </Button>
              </div>
            )}
          </div>
        </>
      )}
    </AppShell>
  );
}
