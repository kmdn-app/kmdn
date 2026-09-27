import { useEffect } from "react";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { Loader2, Play, TriangleAlert } from "lucide-react";
import { AppShell } from "@/components/shell/app-shell";
import { TopBar } from "@/components/shell/top-bar";
import { FindingCard, useFindingActions } from "@/components/consistency/findings";
import { Time } from "@/components/time";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { api, errorMessage, unwrap, useMe } from "@/lib/api";
import { realtime } from "@/lib/realtime";
import { useRepo } from "@/lib/use-repo";
import { cn } from "@/lib/utils";

const KINDS = ["contradiction", "duplicate"] as const;
const STATUSES = ["open", "ignored", "closed"] as const;
type Search = { kind?: (typeof KINDS)[number]; status?: (typeof STATUSES)[number] };

export const Route = createFileRoute("/_app/$owner/$repo/consistency")({
  validateSearch: (s: Record<string, unknown>): Search => ({
    ...(KINDS.includes(s.kind as never) ? { kind: s.kind as Search["kind"] } : {}),
    ...(STATUSES.includes(s.status as never) ? { status: s.status as Search["status"] } : {}),
  }),
  component: ConsistencyReport,
});

/** The repo's consistency report (docs/specs/08-assistant.md#repo-scan). */
function ConsistencyReport() {
  const { t } = useTranslation();
  const repo = useRepo();
  const { data: me } = useMe();
  const { kind = "contradiction", status = "open" } = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });
  const qc = useQueryClient();
  const key = ["consistency-report", repo.id];
  const q = useQuery({
    queryKey: key,
    queryFn: () => unwrap(api.GET("/repos/{repo}/consistency", { params: { path: { repo: repo.id } } })),
    refetchInterval: (query) => (query.state.data?.pending ? 4000 : false),
  });
  const invalidate = () => void qc.invalidateQueries({ queryKey: key });
  useEffect(
    () =>
      realtime.follow("repo:" + repo.id, (ev) => {
        if (ev.type === "consistency") void qc.invalidateQueries({ queryKey: ["consistency-report", repo.id] });
      }),
    [qc, repo.id],
  );
  const { actions, dialog } = useFindingActions(repo, invalidate, { canFix: !!q.data?.can_fix, startsRevision: true });
  const run = useMutation({
    mutationFn: () => unwrap(api.POST("/repos/{repo}/consistency/scan", { params: { path: { repo: repo.id } } })),
    onSuccess: invalidate,
    onError: (e) => toast.error(errorMessage(e, t("errors.generic"))),
  });
  const d = q.data;
  const all = d?.findings ?? [];
  const count = (k: string) => all.filter((f) => f.kind === k && f.status === "open").length;
  const list = all.filter((f) => f.kind === kind && f.status === status);
  const scan = d?.scan;
  return (
    <AppShell repo={repo}>
      {(controls) => (
        <>
          <TopBar
            controls={controls}
            title={t("consistency.reportTitle")}
            actions={
              d?.can_run &&
              d.available && (
                <Button size="sm" onClick={() => run.mutate()} disabled={d.pending || run.isPending}>
                  {d.pending ? <Loader2 className="animate-spin" /> : <Play />}
                  {d.pending ? t("consistency.scanning") : t("consistency.run")}
                </Button>
              )
            }
          />
          {dialog}
          <div className="min-h-0 flex-1 overflow-auto">
            <div className="mx-auto grid max-w-[980px] gap-4 px-8 pt-7 pb-20 max-md:px-4">
              <p className="text-[13.5px] text-muted-foreground">{t("consistency.intro")}</p>
              {d && !d.available && (
                <p className="rounded-lg border border-warning/40 bg-warning/5 p-3 text-[13.5px]">{me?.is_instance_admin ? t("consistency.offAdmin") : t("consistency.off")}</p>
              )}
              {d && (
                <div className="text-[12.5px] text-muted-foreground">
                  {scan ? (
                    <>
                      {t("consistency.lastScan")} <Time iso={scan.started_at} /> · {t("consistency.passagesCompared", { count: scan.passages })}
                    </>
                  ) : (
                    t("consistency.never")
                  )}
                  {scan?.status === "capped" && <p className="mt-1">{t("consistency.capped")}</p>}
                  {scan?.status === "error" && (
                    <p className="mt-1 flex items-center gap-1.5 text-destructive">
                      <TriangleAlert className="size-3.5" />
                      {t("consistency.scanError", { error: scan.error })}
                    </p>
                  )}
                </div>
              )}
              <div className="flex flex-wrap items-center gap-1">
                <div role="tablist" aria-label={t("consistency.title")} className="flex flex-wrap gap-1">
                  {KINDS.map((k) => (
                    <button
                      key={k}
                      role="tab"
                      type="button"
                      aria-selected={kind === k}
                      onClick={() => void navigate({ search: (s) => ({ ...s, kind: k === "contradiction" ? undefined : k }), replace: true })}
                      className={cn("h-8 rounded-full px-3 text-[13px] text-muted-foreground hover:bg-accent", kind === k && "bg-accent font-medium text-foreground")}
                    >
                      {t(`consistency.${k}s`)}
                      {d && <span className="ml-1.5 tabular-nums opacity-70">{count(k)}</span>}
                    </button>
                  ))}
                </div>
                <div role="tablist" aria-label={t("consistency.statusLabel")} className="ml-auto flex gap-1">
                  {STATUSES.map((s) => (
                    <button
                      key={s}
                      role="tab"
                      type="button"
                      aria-selected={status === s}
                      onClick={() => void navigate({ search: (x) => ({ ...x, status: s === "open" ? undefined : s }), replace: true })}
                      className={cn("h-7 rounded-md px-2.5 text-[12.5px] text-muted-foreground hover:bg-accent", status === s && "bg-accent font-medium text-foreground")}
                    >
                      {t(`consistency.status.${s}`)}
                    </button>
                  ))}
                </div>
              </div>
              <div className="overflow-hidden rounded-xl border">
                {q.isLoading &&
                  [0, 1].map((i) => (
                    <div key={i} className="grid gap-2 border-b p-4 last:border-b-0">
                      <Skeleton className="h-4 w-1/2" />
                      <Skeleton className="h-16 w-full" />
                    </div>
                  ))}
                {d && list.length === 0 && <p className="p-8 text-center text-[13.5px] text-muted-foreground">{status === "open" && all.length === 0 ? t("consistency.nothing") : t("consistency.nothingHere")}</p>}
                {list.map((f) => (
                  <FindingCard key={f.id} repo={repo} f={f} actions={actions} />
                ))}
              </div>
            </div>
          </div>
        </>
      )}
    </AppShell>
  );
}
