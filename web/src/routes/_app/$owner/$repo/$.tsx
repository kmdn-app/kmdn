import { useMemo } from "react";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { ChevronRight, History as HistoryIcon, Pencil, ScanText, X } from "lucide-react";
import { AppShell } from "@/components/shell/app-shell";
import { TopBar } from "@/components/shell/top-bar";
import { Avatar } from "@/components/avatar";
import { Time } from "@/components/time";
import { DocView, type BlockLines } from "@/components/doc/doc-view";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { ApiError } from "@/lib/api";
import { fileHref, rawUrl, useBlame, useFile, useHistory, type BlameLine, type Commit, type RepoView } from "@/lib/repos";
import { useRepo } from "@/lib/use-repo";
import { cn } from "@/lib/utils";

type Search = { sha?: string; view?: "blame" };

export const Route = createFileRoute("/_app/$owner/$repo/$")({
  validateSearch: (s: Record<string, unknown>): Search => ({
    ...(typeof s.sha === "string" && /^[0-9a-f]{7,40}$/.test(s.sha) ? { sha: s.sha } : {}),
    ...(s.view === "blame" ? { view: "blame" as const } : {}),
  }),
  component: FilePage,
});

function FilePage() {
  const { t } = useTranslation();
  const repo = useRepo();
  const { _splat: path = "" } = Route.useParams();
  const { sha, view } = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });
  const file = useFile(repo, path, sha);
  const history = useHistory(repo, path);
  const blame = useBlame(repo, path, view === "blame" && !sha);
  const latest = history.data?.[0];
  const oldVersion = sha ? history.data?.find((c) => c.sha.startsWith(sha)) : undefined;
  const crumbs = path.split("/");
  const ctx = useMemo(
    () => ({ path, pageHref: (p: string) => fileHref(repo, p), imageSrc: (p: string) => rawUrl(repo, p, sha) }),
    [repo, path, sha],
  );
  const aside = useMemo(() => (blame.data ? blameAside(blame.data) : undefined), [blame.data]);

  const panel = {
    initial: "history" as const,
    history: <HistoryPanel repo={repo} path={path} commits={history.data} current={sha} />,
  };

  return (
    <AppShell repo={repo} currentPath={path} panel={panel}>
      {(controls) => (
        <>
          <TopBar
            controls={controls}
            title={
              <span className="flex min-w-0 items-center gap-1.5 text-muted-foreground">
                {crumbs.slice(0, -1).map((c, i) => (
                  <span key={i} className="flex min-w-0 shrink-[2] items-center gap-1.5 max-md:hidden">
                    <span className="truncate">{c}</span>
                    <ChevronRight className="size-3 shrink-0 opacity-60" />
                  </span>
                ))}
                <span className="min-w-[3ch] shrink-0 truncate text-foreground max-w-full">{crumbs[crumbs.length - 1]}</span>
              </span>
            }
            actions={
              <>
                <span className="inline-flex h-7 shrink-0 items-center gap-1.5 rounded-full border px-2.5 text-[12.5px] font-medium whitespace-nowrap max-sm:hidden">
                  <span className={cn("size-1.5 rounded-full", sha ? "bg-warning" : "bg-success")} />
                  {sha ? t("file.oldVersion") : t("shell.published")}
                </span>
                {!sha && file.data?.markdown && (
                  <Button
                    variant={view === "blame" ? "secondary" : "ghost"}
                    size="sm"
                    aria-pressed={view === "blame"}
                    onClick={() => void navigate({ search: view === "blame" ? {} : { view: "blame" }, replace: true })}
                  >
                    <ScanText />
                    <span className="max-md:hidden">{t("file.blame")}</span>
                  </Button>
                )}
                <Tooltip>
                  <TooltipTrigger asChild>
                    <span>
                      <Button size="sm" disabled>
                        <Pencil />
                        {t("file.edit")}
                      </Button>
                    </span>
                  </TooltipTrigger>
                  <TooltipContent>{t("shell.revisionsSoon")}</TooltipContent>
                </Tooltip>
              </>
            }
          />
          <div className="min-h-0 flex-1 overflow-auto">
            {sha && (
              <div className="mx-auto mt-5 flex max-w-[720px] items-start gap-3 rounded-xl border bg-muted/40 px-4 py-3 text-[13.5px] max-md:mx-4">
                <HistoryIcon className="mt-0.5 size-4 text-muted-foreground" />
                <div className="flex-1">
                  <div className="font-medium">{oldVersion ? t("file.versionFrom", { title: oldVersion.title }) : t("file.olderVersion")}</div>
                  {oldVersion && (
                    <div className="text-muted-foreground">
                      {oldVersion.author_name} · <Time iso={oldVersion.date} />
                    </div>
                  )}
                </div>
                <Button asChild variant="outline" size="sm">
                  <Link to="/$owner/$repo/$" params={{ owner: repo.owner, repo: repo.name, _splat: path }}>
                    <X />
                    {t("file.backToPublished")}
                  </Link>
                </Button>
              </div>
            )}
            {file.isLoading && (
              <div className="doc grid gap-3">
                <Skeleton className="h-9 w-2/3" />
                <Skeleton className="h-4 w-full" />
                <Skeleton className="h-4 w-5/6" />
                <Skeleton className="h-4 w-4/6" />
              </div>
            )}
            {file.error && (
              <div className="doc">
                <h1>{file.error instanceof ApiError && file.error.status === 404 ? t("errors.notFoundTitle") : t("errors.generic")}</h1>
                <p className="text-muted-foreground">{file.error instanceof ApiError && file.error.status === 404 ? t("file.missing") : file.error.message}</p>
              </div>
            )}
            {file.data && !file.data.markdown && (
              <div className="doc">
                <img src={rawUrl(repo, path, sha)} alt={path} />
              </div>
            )}
            {file.data?.markdown && (
              <>
                {!sha && latest && view !== "blame" && <Byline commit={latest} onHistory={controls.togglePanel} />}
                <DocView markdown={file.data.content} ctx={ctx} aside={view === "blame" ? aside : undefined} className={cn(!sha && latest && view !== "blame" && "pt-0")} />
              </>
            )}
          </div>
        </>
      )}
    </AppShell>
  );
}

function Byline({ commit, onHistory }: { commit: Commit; onHistory: () => void }) {
  const { t } = useTranslation();
  const people = [{ name: commit.author_name, email: commit.author_email }, ...commit.co_authors].filter((p, i, a) => a.findIndex((q) => q.name === p.name) === i);
  const authorsOnly = people.filter((p) => !/\[bot\]|^kmdn$/i.test(p.name));
  const shown = authorsOnly.length ? authorsOnly : people;
  return (
    <div className="mx-auto flex max-w-[720px] flex-wrap items-center gap-2.5 px-6 pt-10 text-[13px] text-muted-foreground max-md:px-4 max-md:pt-7">
      <span className="flex -space-x-1.5">
        {shown.slice(0, 4).map((p) => (
          <Avatar key={p.name} name={p.name} id={p.email || p.name} size="sm" className="ring-2 ring-background" />
        ))}
      </span>
      <span>
        {t("file.lastUpdated")} <Time iso={commit.date} className="font-medium text-foreground" /> {t("file.by")}{" "}
        <b className="font-medium text-foreground">{shown[0]?.name}</b>
        {shown.length > 1 && ` ${t("file.withOthers", { names: shown.slice(1, 3).map((p) => p.name).join(", ") })}`}
      </span>
      <span>·</span>
      <button type="button" className="underline decoration-border underline-offset-4 hover:decoration-current" onClick={onHistory}>
        {t("file.history")}
      </button>
    </div>
  );
}

function blameAside(lines: BlameLine[]) {
  return (_i: number, range: BlockLines) => {
    let best: BlameLine | undefined;
    for (let l = range.start; l <= range.end; l++) {
      const b = lines[l - 1];
      if (b && (!best || b.time > best.time)) best = b;
    }
    if (!best) return null;
    return (
      <div className="flex items-start gap-2 border-r-2 pr-3" style={{ borderColor: "var(--border)" }}>
        <Avatar name={best.author} id={best.email || best.author} size="xs" />
        <div className="min-w-0">
          <b className="block truncate font-medium text-foreground">{best.author}</b>
          {new Date(best.time * 1000).toLocaleDateString("en", { month: "short", day: "numeric", year: "numeric" })}
        </div>
      </div>
    );
  };
}

function HistoryPanel({ repo, path, commits, current }: { repo: RepoView; path: string; commits?: Commit[]; current?: string }) {
  const { t } = useTranslation();
  if (!commits) return <Skeleton className="h-24 w-full" />;
  return (
    <div>
      <div className="mb-3 flex items-center justify-between text-[12.5px] text-muted-foreground">
        <span>{t("file.publishedVersions", { count: commits.length })}</span>
      </div>
      <ol className="relative grid gap-4 pl-7">
        {commits.map((c, i) => {
          const isCurrent = current ? c.sha.startsWith(current) : i === 0;
          return (
            <li key={c.sha} className="relative">
              <span className="absolute top-0 -left-7">
                <Avatar name={c.author_name} id={c.author_email || c.author_name} size="sm" />
              </span>
              <div className="flex items-start justify-between gap-2">
                <div className="min-w-0">
                  <div className="font-medium">{c.title}</div>
                  <div className="text-xs text-muted-foreground">
                    <Time iso={c.date} /> · {c.author_name}
                    {c.co_authors.length > 0 && ` ${t("file.withOthers", { names: c.co_authors.map((p) => p.name.split(" ")[0]).join(", ") })}`}
                  </div>
                </div>
                {isCurrent && <span className="shrink-0 rounded-md bg-success/10 px-1.5 text-[11px] font-medium text-success">{i === 0 ? t("file.current") : t("file.viewing")}</span>}
              </div>
              <div className="mt-1.5 flex flex-wrap items-center gap-1.5 text-[11.5px] text-muted-foreground">
                {c.reviewed_by.length > 0 && <span className="rounded-md border px-1.5">{t("file.reviewedBy", { name: c.reviewed_by[0]!.name })}</span>}
                {c.path && c.path !== path && <span className="rounded-md border px-1.5">{t("file.renamedFrom", { path: c.path })}</span>}
                <span className="font-mono">{c.sha.slice(0, 7)}</span>
              </div>
              {!isCurrent && (
                <Button asChild variant="outline" size="sm" className="mt-2 h-7 text-xs">
                  <Link to="/$owner/$repo/$" params={{ owner: repo.owner, repo: repo.name, _splat: c.path || path }} search={i === 0 ? {} : { sha: c.sha.slice(0, 12) }}>
                    {t("file.view")}
                  </Link>
                </Button>
              )}
            </li>
          );
        })}
      </ol>
    </div>
  );
}
