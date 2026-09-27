import { createFileRoute, Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { AlertTriangle, FileText, Loader2, MessageSquare, RefreshCw, Settings } from "lucide-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AppShell } from "@/components/shell/app-shell";
import { TopBar } from "@/components/shell/top-bar";
import { Avatar } from "@/components/avatar";
import { Time } from "@/components/time";
import { Button } from "@/components/ui/button";
import { ForgeIcon } from "@/components/shell/forge-icon";
import { api, unwrap } from "@/lib/api";
import { atLeast, useActivity, useTree } from "@/lib/repos";
import { useRepo } from "@/lib/use-repo";

export const Route = createFileRoute("/_app/$owner/$repo/")({
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
                  <Link to="/$owner/$repo/settings" params={{ owner: repo.owner, repo: repo.name }}>
                    <Settings />
                    <span className="max-sm:hidden">{t("shell.repoSettings")}</span>
                  </Link>
                </Button>
              )
            }
          />
          <div className="min-h-0 flex-1 overflow-auto">
            <div className="mx-auto max-w-[900px] px-8 py-10 max-sm:px-4">
              <div className="flex items-center gap-2 text-[13px] text-muted-foreground">
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
              <h1 className="mt-2 text-[28px] font-semibold tracking-tight">{repo.display_name}</h1>

              {repo.health === "pending" && (
                <div role="status" className="mt-6 flex items-center gap-3 rounded-xl border p-4">
                  <Loader2 className="size-4 animate-spin" />
                  <div>
                    <div className="font-medium">{t("repo.syncingTitle")}</div>
                    <div className="text-[13px] text-muted-foreground">{t("repo.syncingBody")}</div>
                  </div>
                </div>
              )}
              {(repo.health === "degraded" || repo.health === "disconnected") && (
                <div role="alert" className="mt-6 flex items-start gap-3 rounded-xl border border-destructive/30 bg-destructive/5 p-4">
                  <AlertTriangle className="mt-0.5 size-4 text-destructive" />
                  <div className="flex-1">
                    <div className="font-medium">{repo.health === "disconnected" ? t("repo.disconnected") : t("repo.degraded")}</div>
                    <div className="text-[13px] text-muted-foreground">{repo.health_detail}</div>
                  </div>
                  {atLeast(repo.role, "maintainer") && (
                    <Button variant="outline" size="sm" onClick={() => refresh.mutate()} disabled={refresh.isPending}>
                      <RefreshCw />
                      {t("repo.retry")}
                    </Button>
                  )}
                </div>
              )}

              <OpenFeedback />

              <section className="mt-8">
                <h2 className="mb-2 text-[15px] font-semibold">{t("repo.recent")}</h2>
                {activity && activity.length === 0 && <p className="text-muted-foreground">{t("repo.noActivity")}</p>}
                <ul className="divide-y rounded-xl border">
                  {(activity ?? []).map((c) => (
                    <li key={c.sha} className="flex items-start gap-3 px-4 py-3">
                      <span className="mt-0.5 flex -space-x-1.5">
                        {[{ name: c.author_name, email: c.author_email }, ...c.co_authors].slice(0, 3).map((p) => (
                          <Avatar key={p.email + p.name} name={p.name} id={p.email || p.name} size="sm" className="ring-2 ring-background" />
                        ))}
                      </span>
                      <div className="min-w-0 flex-1">
                        <div className="truncate text-[13.5px] font-medium">{c.title}</div>
                        <div className="mt-1 flex flex-wrap gap-x-3 gap-y-1">
                          {c.paths.slice(0, 4).map((p) => (
                            <Link key={p} to="/$owner/$repo/$" params={{ owner: repo.owner, repo: repo.name, _splat: p }} className="inline-flex items-center gap-1 text-[12.5px] text-muted-foreground hover:text-foreground">
                              <FileText className="size-3" />
                              {p.split("/").pop()}
                            </Link>
                          ))}
                          {c.paths.length > 4 && <span className="text-[12.5px] text-muted-foreground">+{c.paths.length - 4}</span>}
                        </div>
                      </div>
                      <Time iso={c.date} className="shrink-0 text-xs whitespace-nowrap text-muted-foreground" />
                    </li>
                  ))}
                </ul>
              </section>
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
      <h2 className="mb-2 text-[15px] font-semibold">{t("repo.feedback")}</h2>
      <ul className="divide-y rounded-xl border">
        {list.slice(0, 6).map((d) => {
          const first = d.comments[0];
          return (
            <li key={d.id}>
              <Link to="/$owner/$repo/$" params={{ owner: repo.owner, repo: repo.name, _splat: d.path }} className="flex items-start gap-3 px-4 py-3 hover:bg-accent/50">
                <MessageSquare className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
                <div className="min-w-0 flex-1">
                  <div className="truncate text-[13.5px] font-medium">{first?.body}</div>
                  <div className="mt-0.5 truncate text-[12.5px] text-muted-foreground">
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
