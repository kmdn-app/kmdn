import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { CircleAlert, CircleCheck, Loader2, RotateCcw, Stethoscope, TriangleAlert } from "lucide-react";
import { Card, Panel } from "@/components/settings-layout";
import { Time } from "@/components/time";
import { Button } from "@/components/ui/button";
import { api, errorMessage, unwrap } from "@/lib/api";
import { cn } from "@/lib/utils";

/** Admin → System: version, database, jobs with retry, and the doctor. */
export function SystemPanel() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const q = useQuery({ queryKey: ["admin-system"], queryFn: () => unwrap(api.GET("/admin/system")), refetchInterval: 15_000 });
  const doctor = useQuery({ queryKey: ["admin-doctor"], queryFn: () => unwrap(api.GET("/admin/system/doctor")), enabled: false });
  const retry = useMutation({
    mutationFn: (id: string) => unwrap(api.POST("/admin/jobs/{id}/retry", { params: { path: { id } } })),
    onSuccess: () => (toast.success(t("system.retried")), void qc.invalidateQueries({ queryKey: ["admin-system"] })),
    onError: (e) => toast.error(errorMessage(e, t("errors.generic"))),
  });
  const d = q.data;
  const kinds = d ? Object.entries(d.jobs).sort(([a], [b]) => a.localeCompare(b)) : [];
  return (
    <Panel title={t("system.title")} desc={t("system.desc")}>
      {d && (
        <Card>
          <dl className="grid grid-cols-[repeat(auto-fit,minmax(150px,1fr))] gap-4 text-[13.5px]">
            {[
              [t("system.version"), `${d.version.version} (${d.version.commit.slice(0, 7)})`],
              [t("system.go"), d.version.go],
              [t("system.database"), d.db === "sqlite" ? "SQLite" : "Postgres"],
              [t("system.dataDir"), d.data_dir],
              [t("system.repos"), String(d.repos)],
              [t("system.people"), String(d.users)],
            ].map(([k, v]) => (
              <div key={k} className="min-w-0">
                <dt className="text-[12px] text-muted-foreground">{k}</dt>
                <dd className="truncate font-medium" title={v}>
                  {v}
                </dd>
              </div>
            ))}
            <div>
              <dt className="text-[12px] text-muted-foreground">{t("system.started")}</dt>
              <dd className="font-medium">
                <Time iso={d.started_at} />
              </dd>
            </div>
          </dl>
        </Card>
      )}
      <Card>
        <div className="flex items-center justify-between gap-3">
          <div>
            <div className="font-medium">{t("system.doctor")}</div>
            <p className="text-[13px] text-muted-foreground">{t("system.doctorDesc")}</p>
          </div>
          <Button size="sm" variant="outline" onClick={() => void doctor.refetch()} disabled={doctor.isFetching}>
            {doctor.isFetching ? <Loader2 className="animate-spin" /> : <Stethoscope />}
            {doctor.data ? t("system.runAgain") : t("system.run")}
          </Button>
        </div>
        {doctor.data && (
          <ul className="grid gap-1.5 text-[13px]">
            {doctor.data.checks.map((c) => (
              <li key={c.name} className="flex items-start gap-2">
                {c.status === "ok" ? (
                  <CircleCheck className="mt-0.5 size-4 shrink-0 text-success" />
                ) : c.status === "warn" ? (
                  <TriangleAlert className="mt-0.5 size-4 shrink-0 text-warning" />
                ) : (
                  <CircleAlert className="mt-0.5 size-4 shrink-0 text-destructive" />
                )}
                <span className="w-36 shrink-0 font-medium">{c.name}</span>
                <span className={cn("min-w-0 break-words", c.status === "ok" && "text-muted-foreground")}>{c.detail}</span>
              </li>
            ))}
          </ul>
        )}
      </Card>
      {d && (
        <Card>
          <div className="font-medium">{t("system.jobs")}</div>
          {kinds.length === 0 ? (
            <p className="text-[13px] text-muted-foreground">{t("system.idle")}</p>
          ) : (
            <table className="w-full text-[13px]">
              <thead className="text-left text-[12px] text-muted-foreground">
                <tr>
                  <th className="py-1 font-medium">{t("system.kind")}</th>
                  <th className="py-1 text-right font-medium">{t("system.pending")}</th>
                  <th className="py-1 text-right font-medium">{t("system.running")}</th>
                  <th className="py-1 text-right font-medium">{t("system.failed")}</th>
                </tr>
              </thead>
              <tbody>
                {kinds.map(([k, c]) => (
                  <tr key={k} className="border-t">
                    <td className="py-1.5 font-mono text-[12px]">{k}</td>
                    <td className="py-1.5 text-right tabular-nums">{c.pending ?? 0}</td>
                    <td className="py-1.5 text-right tabular-nums">{c.running ?? 0}</td>
                    <td className={cn("py-1.5 text-right tabular-nums", (c.failed ?? 0) > 0 && "font-medium text-destructive")}>{c.failed ?? 0}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
          {d.failed_jobs.length > 0 && (
            <div className="grid gap-2 border-t pt-3">
              <div className="text-[13px] font-medium">{t("system.failedJobs")}</div>
              {d.failed_jobs.map((j) => (
                <div key={j.id} className="flex items-start gap-3 text-[13px]">
                  <div className="min-w-0 flex-1">
                    <div>
                      <code className="font-mono text-[12px]">{j.kind}</code>{" "}
                      <span className="text-muted-foreground">
                        · {t("system.attempts", { count: j.attempts })} · <Time iso={j.updated_at} />
                      </span>
                    </div>
                    <p className="break-words text-muted-foreground">{j.last_error}</p>
                  </div>
                  <Button size="sm" variant="outline" disabled={retry.isPending} onClick={() => retry.mutate(j.id)}>
                    <RotateCcw />
                    {t("system.retry")}
                  </Button>
                </div>
              ))}
            </div>
          )}
        </Card>
      )}
    </Panel>
  );
}
