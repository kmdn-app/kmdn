import { useState } from "react";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { FilePen, Files } from "lucide-react";
import { AppShell } from "@/components/shell/app-shell";
import { TopBar } from "@/components/shell/top-bar";
import { Avatar } from "@/components/avatar";
import { Time } from "@/components/time";
import { NewRevisionDialog, StatePill } from "@/components/revision/revision-ui";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { atLeast } from "@/lib/repos";
import { useRevisions } from "@/lib/revisions";
import { useRepo } from "@/lib/use-repo";
import { cn } from "@/lib/utils";

const FILTERS = ["open", "mine", "published", "closed", "all"] as const;
type Filter = (typeof FILTERS)[number];

export const Route = createFileRoute("/_app/$org/$owner/$repo/revisions/")({
  validateSearch: (s: Record<string, unknown>): { filter?: Filter } => (FILTERS.includes(s.filter as Filter) ? { filter: s.filter as Filter } : {}),
  component: RevisionsList,
});

function RevisionsList() {
  const { t } = useTranslation();
  const repo = useRepo();
  const { filter = "open" } = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });
  const [newOpen, setNewOpen] = useState(false);
  const list = useRevisions(repo, filter === "mine" ? { mine: true } : { state: filter });
  return (
    <AppShell repo={repo}>
      {(controls) => (
        <>
          <TopBar
            controls={controls}
            title={t("revision.list")}
            actions={
              atLeast(repo.role, "contributor") && (
                <Button size="sm" onClick={() => setNewOpen(true)}>
                  <FilePen />
                  {t("revision.newTitle")}
                </Button>
              )
            }
          />
          <NewRevisionDialog repo={repo} open={newOpen} onOpenChange={setNewOpen} />
          <div className="min-h-0 flex-1 overflow-auto">
            <div className="mx-auto max-w-[55rem] px-8 pt-7 pb-20 max-md:px-4">
              <div role="tablist" aria-label={t("revision.filter")} className="mb-4 flex flex-wrap gap-1">
                {FILTERS.map((f) => (
                  <button
                    key={f}
                    role="tab"
                    type="button"
                    aria-selected={filter === f}
                    onClick={() => void navigate({ search: f === "open" ? {} : { filter: f }, replace: true })}
                    className={cn("h-8 rounded-full px-3 text-[0.8125rem] text-muted-foreground hover:bg-accent", filter === f && "bg-accent font-medium text-foreground")}
                  >
                    {t(`revision.filters.${f}`)}
                  </button>
                ))}
              </div>
              <div className="overflow-hidden rounded-xl border">
                {list.isLoading &&
                  [0, 1, 2].map((i) => (
                    <div key={i} className="border-b p-4 last:border-b-0">
                      <Skeleton className="h-4 w-1/2" />
                      <Skeleton className="mt-2 h-3 w-1/3" />
                    </div>
                  ))}
                {list.data?.length === 0 && <p className="p-8 text-center text-[0.84375rem] text-muted-foreground">{t("revision.none")}</p>}
                {list.data?.map((r) => (
                  <Link
                    key={r.id}
                    to="/$org/$owner/$repo/revisions/$number"
                    params={{ org: repo.org_slug, owner: repo.owner, repo: repo.name, number: String(r.number) }}
                    className="flex items-center gap-4 border-b p-4 last:border-b-0 hover:bg-accent/50"
                  >
                    <div className="min-w-0 flex-1">
                      <div className="flex flex-wrap items-center gap-2">
                        <span className="truncate text-[0.90625rem] font-medium">{r.title}</span>
                        <StatePill rev={r} />
                      </div>
                      <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1 text-[0.78125rem] text-muted-foreground">
                        <span>#{r.number}</span>
                        <span className="inline-flex items-center gap-1">
                          <Files className="size-3.5" />
                          {t("revision.pages", { count: r.file_count })}
                        </span>
                        <span>
                          {t("revision.updated")} <Time iso={r.updated_at} />
                        </span>
                      </div>
                    </div>
                    <div className="flex -space-x-1.5">
                      {r.members.slice(0, 4).map((m) => (
                        <Avatar key={m.user_id} name={m.name} id={m.user_id} size="md" className="ring-2 ring-background" />
                      ))}
                    </div>
                  </Link>
                ))}
              </div>
            </div>
          </div>
        </>
      )}
    </AppShell>
  );
}
