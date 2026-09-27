import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { ChevronRight, GitMerge, Loader2 } from "lucide-react";
import type { components } from "@kmdn/api-client";
import { Time } from "@/components/time";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { SourceDiff } from "@/components/revision/diff-views";
import { api, errorMessage, unwrap } from "@/lib/api";
import { realtime } from "@/lib/realtime";
import type { RevisionView } from "@/lib/revisions";
import { cn } from "@/lib/utils";

export type RevisionUpdate = components["schemas"]["RevisionUpdate"];

/** The revision's pending update from Published, kept live. */
export function useRevisionUpdate(rev: RevisionView | undefined) {
  const qc = useQueryClient();
  const id = rev?.id;
  useEffect(() => {
    if (!id) return;
    return realtime.follow("revision:" + id, (ev) => {
      if (ev.type === "update" || ev.type === "revision") void qc.invalidateQueries({ queryKey: ["revision-update", id] });
    });
  }, [qc, id]);
  return useQuery({
    queryKey: ["revision-update", id],
    queryFn: () => unwrap(api.GET("/revisions/{revision}/updates", { params: { path: { revision: id! } } })),
    enabled: !!id && rev?.state !== "published" && rev?.state !== "closed",
  });
}

/**
 * "Published changed 2 pages in this revision · Review and apply"
 * (docs/specs/06-git-and-forges.md#2-tell-people).
 */
export function UpdatesBanner({ rev, className }: { rev: RevisionView; className?: string }) {
  const { t } = useTranslation();
  const q = useRevisionUpdate(rev);
  const [open, setOpen] = useState(false);
  const u = q.data?.update;
  if (!u) return null;
  return (
    <>
      <div className={cn("flex items-center gap-3 rounded-xl border border-sky-500/30 bg-sky-500/5 px-4 py-3 text-[13.5px]", className)}>
        <GitMerge className="size-4 shrink-0 text-sky-500" />
        <div className="min-w-0 flex-1">
          <span className="font-medium">{t("updates.banner", { count: u.files.length })}</span>
          {u.last_commit && (
            <span className="text-muted-foreground">
              {" · "}
              {u.last_commit.author_name}, <Time iso={u.last_commit.date} />
            </span>
          )}
          <p className="text-[12.5px] text-muted-foreground">{t("updates.blocking")}</p>
        </div>
        <Button size="sm" variant="outline" onClick={() => setOpen(true)}>
          {t("updates.review")}
        </Button>
      </div>
      <UpdatesDialog rev={rev} update={u} canApply={q.data?.can_apply ?? false} open={open} onOpenChange={setOpen} />
    </>
  );
}

function UpdatesDialog({ rev, update, canApply, open, onOpenChange }: { rev: RevisionView; update: RevisionUpdate; canApply: boolean; open: boolean; onOpenChange: (v: boolean) => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [shown, setShown] = useState<string | null>(update.files[0]?.path ?? null);
  const apply = useMutation({
    mutationFn: () => unwrap(api.POST("/revisions/{revision}/updates/{update}/apply", { params: { path: { revision: rev.id, update: update.id } } })),
    onSuccess: (r) => {
      onOpenChange(false);
      toast.success(r.conflicts ? t("updates.appliedConflicts", { count: r.conflicts }) : t("updates.applied", { count: r.files }));
      void qc.invalidateQueries({ queryKey: ["revision-update", rev.id] });
      void qc.invalidateQueries({ queryKey: ["revision"] });
      void qc.invalidateQueries({ queryKey: ["revision-files", rev.id] });
    },
    onError: (e) => toast.error(errorMessage(e, t("errors.generic"))),
  });
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-[760px]">
        <DialogHeader>
          <DialogTitle>{t("updates.title")}</DialogTitle>
          <DialogDescription>
            {t("updates.pages", { count: update.files.length })} {update.conflicts ? t("updates.conflictCount", { count: update.conflicts }) : t("updates.noConflicts")}
          </DialogDescription>
        </DialogHeader>
        <ul className="grid gap-2">
          {update.files.map((f) => (
            <li key={f.path} className="rounded-lg border">
              <button type="button" className="flex w-full items-center gap-2 px-3 py-2 text-left text-[13px]" onClick={() => setShown(shown === f.path ? null : f.path)} aria-expanded={shown === f.path}>
                <ChevronRight className={cn("size-3.5 shrink-0 text-muted-foreground transition-transform", shown === f.path && "rotate-90")} />
                <span className="min-w-0 flex-1 truncate font-mono text-[12.5px]">{f.path}</span>
                {f.kind === "merge" && (
                  <span className="text-[12px] text-muted-foreground tabular-nums">
                    <span className="text-success">+{f.additions}</span> <span className="text-destructive">−{f.deletions}</span>
                  </span>
                )}
                <Badge variant={f.conflicts ? "destructive" : "secondary"}>{f.kind === "merge" ? (f.conflicts ? t("updates.conflicts", { count: f.conflicts }) : t("updates.clean")) : t(`updates.kinds.${f.kind}`)}</Badge>
              </button>
              {shown === f.path && f.hunks.length > 0 && (
                <div className="max-h-[320px] overflow-auto border-t">
                  <SourceDiff hunks={f.hunks} />
                </div>
              )}
            </li>
          ))}
        </ul>
        <p className="text-[12.5px] text-muted-foreground">{update.conflicts ? t("updates.noteConflicts") : t("updates.note")}</p>
        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)}>
            {t("updates.notNow")}
          </Button>
          {canApply ? (
            <Button onClick={() => apply.mutate()} disabled={apply.isPending}>
              {apply.isPending && <Loader2 className="animate-spin" />}
              {t("updates.apply")}
            </Button>
          ) : (
            <span className="self-center text-[12.5px] text-muted-foreground">{t(rev.state === "editing" ? "updates.waitEditors" : "updates.waitReviewers")}</span>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
