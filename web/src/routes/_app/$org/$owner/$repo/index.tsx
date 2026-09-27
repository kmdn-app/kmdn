import { useState } from "react";
import { createFileRoute, Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { AlertTriangle, ArrowUp, FilePen, FileText, Files, Loader2, MessageSquare, RefreshCw, Settings, Sparkles } from "lucide-react";
import { useFollowUpdates } from "@/lib/follows";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AppShell } from "@/components/shell/app-shell";
import { TopBar } from "@/components/shell/top-bar";
import { Avatar } from "@/components/avatar";
import { Time } from "@/components/time";
import { showAssistantThread } from "@/components/assistant/assistant-panel";
import { NewRevisionDialog, StateDot } from "@/components/revision/revision-ui";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { ForgeIcon } from "@/components/shell/forge-icon";
import { api, errorMessage, unwrap } from "@/lib/api";
import { atLeast, useActivity, useTree, type RepoView } from "@/lib/repos";
import { useRevisions, type RevisionView } from "@/lib/revisions";
import { useRepo } from "@/lib/use-repo";
import { useAssistantStatus } from "@/lib/orgs";

export const Route = createFileRoute("/_app/$org/$owner/$repo/")({
  component: RepoHome,
});

function RepoHome() {
  const { t } = useTranslation();
  const repo = useRepo();
  const { data: tree } = useTree(repo);
  const { data: activity } = useActivity(repo);
  const qc = useQueryClient();
  const refresh = useMutation({
    mutationFn: () => unwrap(api.POST("/repos/{repo}/refresh", { params: { path: { repo: repo.id } } })),
    onSuccess: () => setTimeout(() => void qc.invalidateQueries({ queryKey: ["repo"] }), 1500),
  });
  const pages = (tree?.items ?? []).filter((n) => n.type === "file" && n.markdown).length;
  return (
    <AppShell repo={repo}>
      {(controls) => (
        <>
          <TopBar
            controls={controls}
            title={repo.display_name}
            actions={
              atLeast(repo.role, "admin") && (
                <Button asChild variant="ghost" size="sm">
                  <Link to="/$org/$owner/$repo/settings" params={{ org: repo.org_slug, owner: repo.owner, repo: repo.name }}>
                    <Settings />
                    <span className="max-sm:hidden">{t("shell.repoSettings")}</span>
                  </Link>
                </Button>
              )
            }
          />
          <div className="min-h-0 flex-1 overflow-auto">
            <div className="mx-auto max-w-[65rem] px-8 py-10 max-sm:px-4">
              <Ask
                repo={repo}
                meta={
                  <div className="flex items-center gap-2 text-[0.8125rem] text-muted-foreground">
                    <ForgeIcon kind={repo.forge_kind} className="size-3.5" />
                    {repo.web_url ? (
                      <a href={repo.web_url} target="_blank" rel="noopener noreferrer" className="hover:underline">
                        {repo.slug}
                      </a>
                    ) : (
                      repo.slug
                    )}
                    <span>·</span>
                    <span>{t("repo.pages", { count: pages })}</span>
                    {repo.last_fetch_at && (
                      <>
                        <span>·</span>
                        <span>
                          {t("repo.synced")} <Time iso={repo.last_fetch_at} />
                        </span>
                      </>
                    )}
                  </div>
                }
              />

              {repo.health === "pending" && (
                <div role="status" className="mt-6 flex items-center gap-3 rounded-xl border p-4">
                  <Loader2 className="size-4 animate-spin" />
                  <div>
                    <div className="font-medium">{t("repo.syncingTitle")}</div>
                    <div className="text-[0.8125rem] text-muted-foreground">{t("repo.syncingBody")}</div>
                  </div>
                </div>
              )}
              {(repo.health === "degraded" || repo.health === "disconnected") && (
                <div role="alert" className="mt-6 flex items-start gap-3 rounded-xl border border-destructive/30 bg-destructive/5 p-4">
                  <AlertTriangle className="mt-0.5 size-4 text-destructive" />
                  <div className="flex-1">
                    <div className="font-medium">{repo.health === "disconnected" ? t("repo.disconnected") : t("repo.degraded")}</div>
                    <div className="text-[0.8125rem] text-muted-foreground">{repo.health_detail}</div>
                  </div>
                  {atLeast(repo.role, "maintainer") && (
                    <Button variant="outline" size="sm" onClick={() => refresh.mutate()} disabled={refresh.isPending}>
                      <RefreshCw />
                      {t("repo.retry")}
                    </Button>
                  )}
                </div>
              )}

              <div className="mt-10 grid items-start gap-4 md:grid-cols-3">
                <ReviewsCard repo={repo} />
                <MineCard repo={repo} />
                <Card title={t("repo.recent")} desc={t("home.recentDesc")}>
                  {activity && activity.length === 0 && <Empty>{t("repo.noActivity")}</Empty>}
                  {(activity ?? []).slice(0, 5).map((c) => {
                    const first = c.paths[0];
                    const row = (
                      <>
                        <FileText className="mt-0.5 size-3.5 shrink-0 text-muted-foreground" />
                        <div className="min-w-0 flex-1">
                          <div className="truncate text-[0.84375rem] font-medium">{c.title}</div>
                          <div className="truncate text-[0.75rem] text-muted-foreground">
                            {first ?? ""}
                            {c.paths.length > 1 && ` +${c.paths.length - 1}`} · <Time iso={c.date} />
                          </div>
                        </div>
                        <Avatar name={c.author_name} id={c.author_email || c.author_name} size="sm" />
                      </>
                    );
                    return first ? (
                      <Link key={c.sha} to="/$org/$owner/$repo/$" params={{ org: repo.org_slug, owner: repo.owner, repo: repo.name, _splat: first }} className={rowCls}>
                        {row}
                      </Link>
                    ) : (
                      <div key={c.sha} className={rowCls}>
                        {row}
                      </div>
                    );
                  })}
                </Card>
              </div>

              <FollowedUpdates />
              <OpenFeedback />
            </div>
          </div>
        </>
      )}
    </AppShell>
  );
}

/** Open discussions on published pages, most active first. */
function OpenFeedback() {
  const { t } = useTranslation();
  const repo = useRepo();
  const q = useQuery({
    queryKey: ["discussions", repo.id, "open"],
    queryFn: async () => (await unwrap(api.GET("/repos/{repo}/discussions", { params: { path: { repo: repo.id }, query: { state: "open" } } }))).items,
  });
  const list = q.data ?? [];
  if (!list.length) return null;
  return (
    <section className="mt-8">
      <h2 className="mb-2 text-[0.9375rem] font-semibold">{t("repo.feedback")}</h2>
      <ul className="divide-y rounded-xl border">
        {list.slice(0, 6).map((d) => {
          const first = d.comments[0];
          return (
            <li key={d.id}>
              <Link to="/$org/$owner/$repo/$" params={{ org: repo.org_slug, owner: repo.owner, repo: repo.name, _splat: d.path }} className="flex items-start gap-3 px-4 py-3 hover:bg-accent/50">
                <MessageSquare className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
                <div className="min-w-0 flex-1">
                  <div className="truncate text-[0.84375rem] font-medium">{first?.body}</div>
                  <div className="mt-0.5 truncate text-[0.78125rem] text-muted-foreground">
                    {first?.author_name} · {d.path}
                    {d.comments.length > 1 && ` · ${t("comments.activity", { count: d.comments.length - 1 })}`}
                  </div>
                </div>
                <Time iso={d.last_activity_at} className="shrink-0 text-xs whitespace-nowrap text-muted-foreground" />
              </Link>
            </li>
          );
        })}
      </ul>
    </section>
  );
}

/** Followed pages that changed since the person last read them. */
function FollowedUpdates() {
  const { t } = useTranslation();
  const repo = useRepo();
  const q = useFollowUpdates(repo.id);
  const list = q.data ?? [];
  if (!list.length) return null;
  return (
    <section className="mt-8">
      <h2 className="mb-2 text-[0.9375rem] font-semibold">{t("follow.updatesTitle")}</h2>
      <ul className="divide-y rounded-xl border">
        {list.map((u) => (
          <li key={u.path}>
            <Link to="/$org/$owner/$repo/$" params={{ org: repo.org_slug, owner: repo.owner, repo: repo.name, _splat: u.path }} className="flex items-start gap-3 px-4 py-3 hover:bg-accent/50">
              <Sparkles className="mt-0.5 size-4 shrink-0 text-success" />
              <div className="min-w-0 flex-1">
                <div className="truncate text-[0.84375rem] font-medium">{u.path.split("/").pop()}</div>
                <div className="mt-0.5 truncate text-[0.78125rem] text-muted-foreground">
                  {u.author_name} · {u.title}
                </div>
              </div>
              <Time iso={u.date} className="shrink-0 text-xs whitespace-nowrap text-muted-foreground" />
            </Link>
          </li>
        ))}
      </ul>
    </section>
  );
}

const rowCls = "flex items-start gap-2.5 px-3.5 py-2.5 hover:bg-accent/50";

function Card({ title, desc, count, action, children }: { title: string; desc: string; count?: number; action?: React.ReactNode; children: React.ReactNode }) {
  return (
    <section className="overflow-hidden rounded-xl border">
      <div className="flex items-start gap-2 border-b px-3.5 py-3">
        <div className="min-w-0 flex-1">
          <h2 className="flex items-center gap-2 text-[0.875rem] font-semibold">
            {title}
            {!!count && <span className="rounded-full bg-primary/10 px-1.5 text-[0.71875rem] font-medium text-primary tabular-nums">{count}</span>}
          </h2>
          <p className="text-[0.75rem] text-muted-foreground">{desc}</p>
        </div>
        {action}
      </div>
      <div className="divide-y">{children}</div>
    </section>
  );
}

function Empty({ children }: { children: React.ReactNode }) {
  return <p className="px-3.5 py-4 text-[0.8125rem] text-muted-foreground">{children}</p>;
}

function RevisionRow({ repo, rev }: { repo: RepoView; rev: RevisionView }) {
  const { t } = useTranslation();
  const owner = rev.members[0];
  return (
    <Link to="/$org/$owner/$repo/revisions/$number" params={{ org: repo.org_slug, owner: repo.owner, repo: repo.name, number: String(rev.number) }} className={rowCls}>
      <StateDot state={rev.state} className="mt-2" />
      <div className="min-w-0 flex-1">
        <div className="truncate text-[0.84375rem] font-medium">{rev.title}</div>
        <div className="flex flex-wrap items-center gap-x-2 text-[0.75rem] text-muted-foreground">
          <span>{t(`revision.state.${rev.state}`)}</span>
          {rev.changes_requested && <span className="text-warning">{t("revision.changesRequested")}</span>}
          {rev.has_conflicts && <span className="text-destructive">{t("revision.conflict")}</span>}
          <span className="inline-flex items-center gap-1">
            <Files className="size-3" />
            {rev.file_count}
          </span>
          <Time iso={rev.updated_at} />
        </div>
      </div>
      {owner && <Avatar name={owner.name} id={owner.user_id} size="sm" />}
    </Link>
  );
}

/** Revisions asking the signed-in person for a review they haven't given. */
function ReviewsCard({ repo }: { repo: RepoView }) {
  const { t } = useTranslation();
  const list = useRevisions(repo, { reviewing: true });
  return (
    <Card title={t("home.reviews")} desc={t("home.reviewsDesc")} count={list.data?.length}>
      {list.data?.length === 0 && <Empty>{t("home.noReviews")}</Empty>}
      {list.data?.slice(0, 5).map((r) => <RevisionRow key={r.id} repo={repo} rev={r} />)}
    </Card>
  );
}

function MineCard({ repo }: { repo: RepoView }) {
  const { t } = useTranslation();
  const list = useRevisions(repo, { mine: true });
  return (
    <Card
      title={t("home.mine")}
      desc={t("home.mineDesc")}
      action={
        <Button asChild variant="ghost" size="sm" className="h-7 px-2 text-[0.78125rem]">
          <Link to="/$org/$owner/$repo/revisions" params={{ org: repo.org_slug, owner: repo.owner, repo: repo.name }} search={{ filter: "mine" }}>
            {t("home.all")}
          </Link>
        </Button>
      }
    >
      {list.data?.length === 0 && <Empty>{t("home.noMine")}</Empty>}
      {list.data?.slice(0, 5).map((r) => <RevisionRow key={r.id} repo={repo} rev={r} />)}
    </Card>
  );
}

/**
 * "What do you want to change?": asks the assistant in a new Q&A thread,
 * shown in the right panel; the assistant proposes a revision for changes.
 */
function Ask({ repo, meta }: { repo: RepoView; meta: React.ReactNode }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const status = useAssistantStatus();
  const [text, setText] = useState("");
  const [newOpen, setNewOpen] = useState(false);
  const canEdit = atLeast(repo.role, "contributor");
  const ask = useMutation({
    mutationFn: () => unwrap(api.POST("/repos/{repo}/assistant/threads", { params: { path: { repo: repo.id } }, body: { text: text.trim(), context: {} } })),
    onSuccess: (r) => {
      setText("");
      qc.setQueryData<{ id: string }[]>(["assistant-threads", repo.id], (old) => (old ? [r.thread, ...old.filter((x) => x.id !== r.thread.id)] : old));
      void qc.invalidateQueries({ queryKey: ["assistant-threads", repo.id] });
      showAssistantThread(r.thread.id);
    },
    onError: (e) => toast.error(errorMessage(e, t("errors.generic"))),
  });
  const newRevision = canEdit && (
    <>
      <Button variant="outline" size="sm" onClick={() => setNewOpen(true)}>
        <FilePen />
        {t("revision.newTitle")}
      </Button>
      <NewRevisionDialog repo={repo} open={newOpen} onOpenChange={setNewOpen} />
    </>
  );
  if (!status.data?.enabled) {
    return (
      <>
        {meta}
        <div className="mt-2 flex flex-wrap items-center justify-between gap-3">
          <h1 className="text-[1.75rem] font-semibold tracking-tight">{repo.display_name}</h1>
          {newRevision}
        </div>
      </>
    );
  }
  const submit = () => {
    if (text.trim() && !ask.isPending) ask.mutate();
  };
  return (
    <div className="mx-auto mt-4 max-w-[45rem]">
      {meta}
      <h1 className="mt-2 text-[1.75rem] font-semibold tracking-tight">{t("home.ask")}</h1>
      <form
        className="relative mt-4"
        onSubmit={(e) => {
          e.preventDefault();
          submit();
        }}
      >
        <Textarea
          rows={3}
          value={text}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
              e.preventDefault();
              submit();
            }
          }}
          placeholder={t("home.askPlaceholder", { name: repo.display_name })}
          aria-label={t("home.ask")}
          className="min-h-24 resize-none rounded-2xl pr-12 pb-10 text-[0.90625rem] shadow-sm"
        />
        <div className="pointer-events-none absolute inset-x-3 bottom-2.5 flex items-center gap-2">
          <span className="inline-flex items-center gap-1 text-[0.75rem] text-muted-foreground">
            <Sparkles className="size-3.5" />
            {t("home.askHint")}
          </span>
          <Button type="submit" size="icon" className="pointer-events-auto ml-auto size-8 rounded-full" disabled={!text.trim() || ask.isPending} aria-label={t("assistant.send")}>
            {ask.isPending ? <Loader2 className="animate-spin" /> : <ArrowUp />}
          </Button>
        </div>
      </form>
      {canEdit && <div className="mt-3 flex justify-end">{newRevision}</div>}
    </div>
  );
}
