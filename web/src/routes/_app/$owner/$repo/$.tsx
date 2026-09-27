import { useCallback, useMemo, useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { Bell, BellRing, ChevronRight, History as HistoryIcon, Loader2, MessageSquarePlus, Pencil, ScanText, Sparkles, X } from "lucide-react";
import { nodeHash, parse } from "@kmdn/doc-engine";
import { usePageRead, useFollowState, useToggleFollow } from "@/lib/follows";
import { AppShell } from "@/components/shell/app-shell";
import { TopBar } from "@/components/shell/top-bar";
import { Avatar } from "@/components/avatar";
import { Time } from "@/components/time";
import { DocView, type BlockLines } from "@/components/doc/doc-view";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { ApiError, api, errorMessage, unwrap } from "@/lib/api";
import { CommentsPanel, type PendingComment } from "@/components/revision/comments-panel";
import { selectionQuote, useBlockHighlights, useQuoteHighlights, type QuoteAnchor } from "@/components/doc/quote-anchors";
import { useDiscussions, type Thread } from "@/lib/threads";
import { pageTitle, useCreateRevision } from "@/lib/revisions";
import { RevisionPage } from "@/components/editor/revision-page";
import { LinksPanel } from "@/components/links-panel";
import { atLeast, fileHref, rawUrl, useBlame, useFile, useHistory, type BlameLine, type Commit, type RepoView } from "@/lib/repos";
import { openPanel, useIsPhone } from "@/lib/media";
import { useRepo } from "@/lib/use-repo";
import { cn } from "@/lib/utils";

type Search = { sha?: string; view?: "blame"; revision?: number };

export const Route = createFileRoute("/_app/$owner/$repo/$")({
  validateSearch: (s: Record<string, unknown>): Search => ({
    ...(typeof s.sha === "string" && /^[0-9a-f]{7,40}$/.test(s.sha) ? { sha: s.sha } : {}),
    ...(s.view === "blame" ? { view: "blame" as const } : {}),
    ...(Number.isInteger(Number(s.revision)) && Number(s.revision) > 0 ? { revision: Number(s.revision) } : {}),
  }),
  component: FilePage,
});

function FilePage() {
  const repo = useRepo();
  const { _splat: path = "" } = Route.useParams();
  const { revision } = Route.useSearch();
  if (revision) return <RevisionPage key={revision} repo={repo} path={path} number={revision} />;
  return <PublishedPage />;
}

function PublishedPage() {
  const { t } = useTranslation();
  const repo = useRepo();
  const { _splat: path = "" } = Route.useParams();
  const { sha, view } = Route.useSearch();
  const create = useCreateRevision(repo);
  const canEdit = atLeast(repo.role, "contributor");
  const phone = useIsPhone();
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
  const aside = useMemo(() => (blame.data ? blameAside(blame.data, (names) => t("file.withOthers", { names })) : undefined), [blame.data, t]);
  // Discussions on the published version (not old ones).
  const discussable = !sha && !!file.data?.markdown;
  const [root, setRoot] = useState<HTMLDivElement | null>(null);
  const [pending, setPending] = useState<PendingComment | null>(null);
  const [active, setActive] = useState<string | null>(null);
  const discussions = useDiscussions(discussable ? repo.id : undefined, path, "hot");
  const anchors = useMemo(
    () => (discussions.data ?? []).filter((d) => d.state === "open" && !d.outdated).map((d) => ({ id: d.id, anchor: d.anchor as QuoteAnchor })),
    [discussions.data],
  );
  const onAnchorClick = useCallback((id: string) => (setActive(id), openPanel()), []);
  useQuoteHighlights(root, anchors, active, onAnchorClick, file.data?.content);
  // "Updated since your last visit": the previous read of this page.
  const lastRead = usePageRead(repo.id, path, discussable ? latest?.sha : undefined);
  const since = lastRead && latest && lastRead.sha !== latest.sha ? lastRead : null;
  const [showChanges, setShowChanges] = useState(false);
  const before = useFile(repo, path, showChanges && since ? since.sha : undefined);
  const changedBlocks = useMemo(() => {
    if (!showChanges || !before.data?.content || !file.data?.content) return null;
    const old = new Map<string, number>();
    for (const b of parse(before.data.content).doc.content) old.set(nodeHash(b), (old.get(nodeHash(b)) ?? 0) + 1);
    const out: number[] = [];
    parse(file.data.content).doc.content.forEach((b, i) => {
      const h = nodeHash(b);
      const n = old.get(h) ?? 0;
      if (n > 0) old.set(h, n - 1);
      else out.push(i);
    });
    return out;
  }, [showChanges, before.data, file.data]);
  useBlockHighlights(root, changedBlocks);
  const fix = useMutation({
    mutationFn: (th: Thread) => unwrap(api.POST("/threads/{thread}/fix-this", { params: { path: { thread: th.id } } })),
    onSuccess: ({ revision }) => {
      toast.success(t("comments.fixStarted", { title: revision.title }));
      void navigate({ search: { revision: revision.number } });
    },
    onError: (e) => toast.error(errorMessage(e, t("errors.generic"))),
  });

  const panel = {
    initial: pending || active ? ("comments" as const) : ("history" as const),
    history: <HistoryPanel repo={repo} path={path} commits={history.data} current={sha} />,
    links: file.data?.markdown ? <LinksPanel repo={repo} path={path} /> : undefined,
    comments: discussable ? (
      <CommentsPanel
        repoID={repo.id}
        path={path}
        pending={pending}
        onPendingDone={() => setPending(null)}
        active={active}
        onActive={setActive}
        canComment
        onFix={canEdit ? (th) => fix.mutate(th) : undefined}
      />
    ) : undefined,
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
                  <span key={i} className="flex min-w-0 shrink-[2] items-center gap-1.5 @max-3xl:hidden">
                    <span className="truncate">{c}</span>
                    <ChevronRight className="size-3 shrink-0 opacity-60" />
                  </span>
                ))}
                <span className="min-w-[3ch] shrink-0 truncate text-foreground max-w-full">{crumbs[crumbs.length - 1]}</span>
              </span>
            }
            actions={
              <>
                <span className="inline-flex h-7 shrink-0 items-center gap-1.5 rounded-full border px-2.5 text-[12.5px] font-medium whitespace-nowrap @max-3xl:hidden">
                  <span className={cn("size-1.5 rounded-full", sha ? "bg-warning" : "bg-success")} />
                  {sha ? t("file.oldVersion") : t("shell.published")}
                </span>
                {discussable && <FollowButton repoID={repo.id} path={path} />}
                {discussable && (
                  <Button
                    size="sm"
                    variant="ghost"
                    title={t("comments.commentOnPage")}
                    onClick={() => {
                      const q = selectionQuote(root);
                      setPending({ quote: q?.quote ?? "", prefix: q?.prefix, suffix: q?.suffix, position: null });
                      if (!controls.panelOpen) controls.togglePanel();
                    }}
                  >
                    <MessageSquarePlus />
                    <span className="@max-4xl:hidden">{t("comments.comment")}</span>
                  </Button>
                )}
                {!sha && file.data?.markdown && (
                  <Button
                    variant={view === "blame" ? "secondary" : "ghost"}
                    size="sm"
                    aria-pressed={view === "blame"}
                    onClick={() => void navigate({ search: view === "blame" ? {} : { view: "blame" }, replace: true })}
                  >
                    <ScanText />
                    <span className="@max-4xl:hidden">{t("file.blame")}</span>
                  </Button>
                )}
                {phone ? null : canEdit && file.data?.markdown ? (
                  <Button
                    size="sm"
                    disabled={create.isPending}
                    onClick={() =>
                      create.mutate(
                        { title: t("revision.editsTo", { title: pageTitle(file.data.content, path) }), path },
                        {
                          onSuccess: (rev) => {
                            toast.success(t("revision.started", { title: rev.title }));
                            void navigate({ search: { revision: rev.number } });
                          },
                          onError: (e) => toast.error(errorMessage(e, t("errors.generic"))),
                        },
                      )
                    }
                  >
                    {create.isPending ? <Loader2 className="animate-spin" /> : <Pencil />}
                    {t("file.edit")}
                  </Button>
                ) : (
                  !canEdit && (
                    <Tooltip>
                      <TooltipTrigger asChild>
                        <span>
                          <Button size="sm" disabled>
                            <Pencil />
                            {t("file.edit")}
                          </Button>
                        </span>
                      </TooltipTrigger>
                      <TooltipContent>{t("file.viewerHint")}</TooltipContent>
                    </Tooltip>
                  )
                )}
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
                {since && latest && view !== "blame" && (
                  <UpdatedBanner
                    repoID={repo.id}
                    path={path}
                    since={since}
                    commits={history.data ?? []}
                    showing={showChanges}
                    loading={showChanges && before.isLoading}
                    changed={changedBlocks?.length}
                    onToggle={() => setShowChanges((v) => !v)}
                  />
                )}
                {!sha && latest && view !== "blame" && <Byline commit={latest} onHistory={controls.togglePanel} />}
                <div ref={setRoot}>
                  <DocView markdown={file.data.content} ctx={ctx} aside={view === "blame" ? aside : undefined} className={cn(!sha && latest && view !== "blame" && "pt-0")} />
                </div>
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

function blameAside(lines: BlameLine[], withOthers: (names: string) => string) {
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
          {best.co_authors.length > 0 && <span className="block truncate">{withOthers(best.co_authors.map((n) => n.split(" ")[0]).join(", "))}</span>}
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

/** Follow the page (bell); following a folder above it shows too. */
function FollowButton({ repoID, path }: { repoID: string; path: string }) {
  const { t } = useTranslation();
  const st = useFollowState(repoID, path);
  const toggle = useToggleFollow(repoID, path);
  const viaFolder = st.data?.following && !st.data.page ? st.data.folder : undefined;
  const on = !!st.data?.page;
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Button
          size="sm"
          variant={on ? "secondary" : "ghost"}
          aria-pressed={on}
          disabled={!st.data || toggle.isPending || !!viaFolder}
          onClick={() => toggle.mutate(!on, { onError: (e) => toast.error(errorMessage(e, t("errors.generic"))) })}
        >
          {st.data?.following ? <BellRing /> : <Bell />}
          <span className="@max-4xl:hidden">{on || viaFolder ? t("follow.following") : t("follow.follow")}</span>
        </Button>
      </TooltipTrigger>
      <TooltipContent>{viaFolder !== undefined ? t("follow.viaFolder", { folder: viaFolder || "/" }) : st.data?.auto_until ? t("follow.auto") : t("follow.hint")}</TooltipContent>
    </Tooltip>
  );
}

/** "Updated since your last visit": who changed the page, what, and a way to see it. */
function UpdatedBanner({ repoID, path, since, commits, showing, loading, changed, onToggle }: { repoID: string; path: string; since: { sha: string; at: string }; commits: Commit[]; showing: boolean; loading: boolean; changed?: number; onToggle: () => void }) {
  const { t } = useTranslation();
  // The assistant's summaries of each change, when it wrote them at publish.
  const summaries = useQuery({
    queryKey: ["change-summaries", repoID, path],
    queryFn: async () => (await unwrap(api.GET("/repos/{repo}/change-summaries", { params: { path: { repo: repoID }, query: { path } } }))).by_commit,
  });
  const at = commits.findIndex((c) => c.sha === since.sha);
  const news = (at >= 0 ? commits.slice(0, at) : commits.filter((c) => c.date > since.at)).slice(0, 5);
  if (!news.length) return null;
  const people = [...new Set(news.flatMap((c) => [c.author_name, ...c.co_authors.map((p) => p.name)]).filter((n) => !/\[bot\]|^kmdn$/i.test(n)))];
  return (
    <div className="mx-auto mt-6 flex max-w-[720px] items-start gap-3 rounded-xl border border-success/30 bg-success/5 px-4 py-3 text-[13.5px] max-md:mx-4">
      <Sparkles className="mt-0.5 size-4 shrink-0 text-success" />
      <div className="min-w-0 flex-1">
        <div className="font-medium">
          {t("follow.updated", { date: new Date(news[0]!.date).toLocaleDateString(undefined, { month: "short", day: "numeric" }), people: people.slice(0, 3).join(", ") })}
        </div>
        <div className="mt-0.5 text-muted-foreground">{news.map((c) => summaries.data?.[c.sha] ?? c.title).join(" · ")}</div>
        {showing && changed === 0 && <div className="mt-1 text-[12.5px] text-muted-foreground">{t("follow.noVisibleChange")}</div>}
      </div>
      <Button size="sm" variant="outline" onClick={onToggle} disabled={loading}>
        {loading && <Loader2 className="animate-spin" />}
        {showing ? t("follow.hideChanges") : t("follow.showChanges")}
      </Button>
    </div>
  );
}
