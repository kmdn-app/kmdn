import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { Check, CircleCheck, CircleDashed, Copy, GitPullRequest, Loader2, MessageSquareWarning, Rocket, Send, Split, TriangleAlert, Undo2, UserPlus, X } from "lucide-react";
import { useRevisionConsistency } from "@/components/consistency/findings";
import { Avatar } from "@/components/avatar";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { api, errorMessage, useMe, unwrap } from "@/lib/api";
import type { RepoView } from "@/lib/repos";
import type { RevisionView } from "@/lib/revisions";
import { cn } from "@/lib/utils";

function useRefresh(repo: RepoView, rev: RevisionView) {
  const qc = useQueryClient();
  return (next?: RevisionView) => {
    if (next) qc.setQueryData(["revision", repo.id, rev.number], next);
    void qc.invalidateQueries({ queryKey: ["revision", repo.id, rev.number] });
    void qc.invalidateQueries({ queryKey: ["revision-events", rev.id] });
    void qc.invalidateQueries({ queryKey: ["revisions", repo.id] });
  };
}

/** "Approved 1 of 2" */
export function ApprovalSummary({ rev, className }: { rev: RevisionView; className?: string }) {
  const { t } = useTranslation();
  if (rev.state !== "in_review" && rev.state !== "approved") return null;
  const approved = rev.reviewers.filter((r) => r.state === "approved").length;
  return (
    <span className={cn("text-[0.75rem] text-muted-foreground", className)}>
      {t("review.approvedOf", { approved, total: rev.reviewers.length })}
    </span>
  );
}

/** The review buttons for whoever is looking: submit, approve, request changes, withdraw. */
export function ReviewActions({ repo, rev, compact }: { repo: RepoView; rev: RevisionView; compact?: boolean }) {
  const { t } = useTranslation();
  const { data: me } = useMe();
  const refresh = useRefresh(repo, rev);
  const [submitOpen, setSubmitOpen] = useState(false);
  const [changesOpen, setChangesOpen] = useState(false);
  const [publishOpen, setPublishOpen] = useState(false);
  const onError = (e: unknown) => toast.error(errorMessage(e, t("errors.generic")));
  const approve = useMutation({
    mutationFn: () => unwrap(api.POST("/revisions/{revision}/approve", { params: { path: { revision: rev.id } } })),
    onSuccess: (r) => {
      refresh(r);
      toast.success(r.state === "approved" ? t("review.nowApproved") : t("review.youApproved"));
    },
    onError,
  });
  const withdraw = useMutation({
    mutationFn: () => unwrap(api.POST("/revisions/{revision}/withdraw", { params: { path: { revision: rev.id } } })),
    onSuccess: (r) => refresh(r),
    onError,
  });
  const a = rev.access;
  const mine = rev.reviewers.find((r) => r.user_id === me?.id);
  return (
    <>
      {rev.state === "editing" && a.can_submit && (
        <Button size="sm" onClick={() => setSubmitOpen(true)}>
          <Send />
          {t("revision.submit")}
        </Button>
      )}
      {(rev.state === "in_review" || rev.state === "approved") && a.can_withdraw && !a.can_review && (
        <Button size="sm" variant="ghost" onClick={() => withdraw.mutate()} disabled={withdraw.isPending}>
          <Undo2 />
          <span className={cn(compact && "max-lg:hidden")}>{t("review.withdraw")}</span>
        </Button>
      )}
      {a.can_review && (
        <>
          <Button size="sm" variant="outline" onClick={() => setChangesOpen(true)}>
            <MessageSquareWarning />
            <span className={cn(compact && "max-lg:hidden")}>{t("review.requestChanges")}</span>
          </Button>
          <Button size="sm" onClick={() => approve.mutate()} disabled={approve.isPending || mine?.state === "approved"}>
            {approve.isPending ? <Loader2 className="animate-spin" /> : <Check />}
            {mine?.state === "approved" ? t("review.approved") : t("review.approve")}
          </Button>
        </>
      )}
      {rev.state === "approved" && a.can_publish && (
        <Button size="sm" onClick={() => setPublishOpen(true)}>
          <Rocket />
          {t("publish.action")}
        </Button>
      )}
      {publishOpen && <PublishDialog repo={repo} rev={rev} onClose={() => setPublishOpen(false)} onDone={refresh} />}
      {submitOpen && <SubmitDialog rev={rev} onClose={() => setSubmitOpen(false)} onDone={refresh} />}
      {changesOpen && <RequestChangesDialog rev={rev} onClose={() => setChangesOpen(false)} onDone={refresh} />}
    </>
  );
}

function SubmitDialog({ rev, onClose, onDone }: { rev: RevisionView; onClose: () => void; onDone: (r: RevisionView) => void }) {
  const { t } = useTranslation();
  const candidates = useQuery({
    queryKey: ["reviewer-suggestions", rev.id],
    queryFn: async () => (await unwrap(api.GET("/revisions/{revision}/reviewer-suggestions", { params: { path: { revision: rev.id } } }))).items,
  });
  const checks = useQuery({
    queryKey: ["revision-checks", rev.id, rev.updated_at],
    queryFn: () => unwrap(api.GET("/revisions/{revision}/checks", { params: { path: { revision: rev.id } } })),
  });
  const [title, setTitle] = useState(rev.title);
  const [description, setDescription] = useState(rev.description);
  const [picked, setPicked] = useState<Set<string>>(() => new Set(rev.reviewers.map((r) => r.user_id)));
  const toggle = (id: string) =>
    setPicked((p) => {
      const n = new Set(p);
      if (n.has(id)) n.delete(id);
      else n.add(id);
      return n;
    });
  const submit = useMutation({
    mutationFn: () =>
      unwrap(api.POST("/revisions/{revision}/submit", { params: { path: { revision: rev.id } }, body: { reviewers: [...picked], title, description } })),
    onSuccess: (r) => {
      onDone(r);
      onClose();
      toast.success(t("review.submitted"));
    },
  });
  const broken = (checks.data?.broken_links.length ?? 0) + (checks.data?.breaks_inbound.length ?? 0);
  return (
    <Dialog open onOpenChange={(v) => !v && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{t("review.submitTitle")}</DialogTitle>
          <DialogDescription>{t("review.submitDesc")}</DialogDescription>
        </DialogHeader>
        <form
          className="grid gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            submit.mutate();
          }}
        >
          <div className="grid gap-1.5">
            <Label htmlFor="sub-title">{t("revision.titleLabel")}</Label>
            <Input id="sub-title" value={title} onChange={(e) => setTitle(e.target.value)} maxLength={200} />
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="sub-desc">{t("review.whatChanged")}</Label>
            <Textarea id="sub-desc" rows={3} value={description} onChange={(e) => setDescription(e.target.value)} placeholder={t("revision.descriptionPlaceholder")} />
          </div>
          <fieldset className="grid gap-1.5">
            <legend className="mb-1.5 text-sm font-medium">{t("review.reviewers")}</legend>
            <div className="max-h-52 overflow-auto rounded-lg border">
              {candidates.data?.length === 0 && <p className="p-3 text-[0.8125rem] text-muted-foreground">{t("review.noCandidates")}</p>}
              {candidates.data?.map((c) => (
                <label key={c.user_id} className="flex cursor-pointer items-center gap-2.5 border-b px-3 py-2 last:border-b-0 hover:bg-accent/50">
                  <input type="checkbox" checked={picked.has(c.user_id)} onChange={() => toggle(c.user_id)} className="accent-primary" />
                  <Avatar name={c.name} id={c.user_id} size="sm" />
                  <span className="min-w-0 flex-1 truncate text-[0.84375rem]">{c.name}</span>
                  {c.suggested && <span className="rounded-full bg-accent px-2 py-0.5 text-[0.6875rem] text-muted-foreground">{t("review.suggested")}</span>}
                </label>
              ))}
            </div>
          </fieldset>
          {broken > 0 && (
            <p className="flex items-start gap-2 rounded-lg border border-warning/40 bg-warning/5 px-3 py-2 text-[0.8125rem]">
              <TriangleAlert className="mt-0.5 size-4 shrink-0 text-warning" />
              {t("review.brokenLinksWarning", { count: broken })}
            </p>
          )}
          {submit.error && <p className="text-[0.8125rem] text-destructive">{errorMessage(submit.error, t("errors.generic"))}</p>}
          <DialogFooter>
            <Button type="button" variant="ghost" onClick={onClose}>
              {t("common.cancel")}
            </Button>
            <Button type="submit" disabled={picked.size === 0 || !title.trim() || submit.isPending}>
              {submit.isPending && <Loader2 className="animate-spin" />}
              {t("review.submitAction", { count: picked.size })}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function RequestChangesDialog({ rev, onClose, onDone }: { rev: RevisionView; onClose: () => void; onDone: (r: RevisionView) => void }) {
  const { t } = useTranslation();
  const [note, setNote] = useState("");
  const send = useMutation({
    mutationFn: () => unwrap(api.POST("/revisions/{revision}/request-changes", { params: { path: { revision: rev.id } }, body: { note } })),
    onSuccess: (r) => {
      onDone(r);
      onClose();
    },
  });
  return (
    <Dialog open onOpenChange={(v) => !v && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t("review.requestChanges")}</DialogTitle>
          <DialogDescription>{t("review.requestChangesDesc")}</DialogDescription>
        </DialogHeader>
        <form
          className="grid gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            send.mutate();
          }}
        >
          <Textarea autoFocus rows={4} value={note} onChange={(e) => setNote(e.target.value)} placeholder={t("review.notePlaceholder")} aria-label={t("review.note")} />
          {send.error && <p className="text-[0.8125rem] text-destructive">{errorMessage(send.error, t("errors.generic"))}</p>}
          <DialogFooter>
            <Button type="button" variant="ghost" onClick={onClose}>
              {t("common.cancel")}
            </Button>
            <Button type="submit" disabled={send.isPending}>
              {t("review.sendBack")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

/** Assigned reviewers with where each stands; editors can add and remove them. */
export function ReviewersSection({ repo, rev }: { repo: RepoView; rev: RevisionView }) {
  const { t } = useTranslation();
  const refresh = useRefresh(repo, rev);
  const [adding, setAdding] = useState(false);
  const candidates = useQuery({
    queryKey: ["reviewer-suggestions", rev.id],
    queryFn: async () => (await unwrap(api.GET("/revisions/{revision}/reviewer-suggestions", { params: { path: { revision: rev.id } } }))).items,
    enabled: adding,
  });
  const add = useMutation({
    mutationFn: (id: string) => unwrap(api.PUT("/revisions/{revision}/reviewers/{user}", { params: { path: { revision: rev.id, user: id } } })),
    onSuccess: () => {
      setAdding(false);
      refresh();
    },
    onError: (e) => toast.error(errorMessage(e, t("errors.generic"))),
  });
  const remove = useMutation({
    mutationFn: (id: string) => unwrap(api.DELETE("/revisions/{revision}/reviewers/{user}", { params: { path: { revision: rev.id, user: id } } })),
    onSuccess: () => refresh(),
    onError: (e) => toast.error(errorMessage(e, t("errors.generic"))),
  });
  const inReview = rev.state === "in_review" || rev.state === "approved";
  if (!inReview && rev.reviewers.length === 0) return null;
  const assigned = new Set(rev.reviewers.map((r) => r.user_id));
  const icon = (state: string) =>
    state === "approved" ? <CircleCheck className="size-4 text-success" /> : state === "changes_requested" ? <MessageSquareWarning className="size-4 text-warning" /> : <CircleDashed className="size-4 text-muted-foreground" />;
  return (
    <section>
      <div className="mb-3 flex items-center justify-between gap-3">
        <h2 className="text-[0.9375rem] font-semibold">
          {t("review.reviewers")} <ApprovalSummary rev={rev} className="ml-1 font-normal" />
        </h2>
        {rev.access.can_submit && inReview && (
          <Button size="sm" variant="outline" onClick={() => setAdding((v) => !v)}>
            <UserPlus />
            {t("review.addReviewer")}
          </Button>
        )}
      </div>
      <div className="overflow-hidden rounded-xl border">
        {rev.reviewers.map((r) => (
          <div key={r.user_id} className="flex items-center gap-3 border-b px-4 py-2.5 last:border-b-0">
            <Avatar name={r.name} id={r.user_id} />
            <span className="min-w-0 flex-1 truncate text-[0.84375rem] font-medium">{r.name}</span>
            <span className="flex items-center gap-1.5 text-[0.78125rem] text-muted-foreground">
              {icon(r.state)}
              {t(`review.states.${r.state}`)}
            </span>
            {rev.access.can_submit && inReview && (
              <Button variant="ghost" size="icon" className="size-7" onClick={() => remove.mutate(r.user_id)} aria-label={t("review.removeReviewer", { name: r.name })}>
                <X />
              </Button>
            )}
          </div>
        ))}
        {adding &&
          candidates.data
            ?.filter((c) => !assigned.has(c.user_id))
            .map((c) => (
              <button key={c.user_id} type="button" onClick={() => add.mutate(c.user_id)} className="flex w-full items-center gap-2.5 border-t px-4 py-2 text-left hover:bg-accent/50">
                <Avatar name={c.name} id={c.user_id} size="sm" />
                <span className="text-[0.84375rem]">{c.name}</span>
                {c.suggested && <span className="text-[0.6875rem] text-muted-foreground">{t("review.suggested")}</span>}
              </button>
            ))}
      </div>
    </section>
  );
}

function PublishDialog({ repo, rev, onClose, onDone }: { repo: RepoView; rev: RevisionView; onClose: () => void; onDone: () => void }) {
  const { t } = useTranslation();
  const preview = useQuery({
    queryKey: ["commit-preview", rev.id, rev.updated_at],
    queryFn: () => unwrap(api.GET("/revisions/{revision}/commit-preview", { params: { path: { revision: rev.id } } })),
  });
  const [title, setTitle] = useState<string | null>(null);
  const [body, setBody] = useState<string | null>(null);
  const p = preview.data;
  const findings = (useRevisionConsistency(rev).data?.findings ?? []).filter((f) => f.status === "open");
  const publish = useMutation({
    mutationFn: () => unwrap(api.POST("/revisions/{revision}/publish", { params: { path: { revision: rev.id } }, body: { title: title ?? p?.title ?? rev.title, body: body ?? p?.body ?? "" } })),
    onSuccess: () => {
      toast.success(t("publish.publishing"));
      onDone();
      onClose();
    },
  });
  return (
    <Dialog open onOpenChange={(v) => !v && onClose()}>
      <DialogContent className="sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>{t("publish.title", { branch: repo.target_branch })}</DialogTitle>
          <DialogDescription>{t("publish.desc")}</DialogDescription>
        </DialogHeader>
        {!p ? (
          <Loader2 className="mx-auto animate-spin" />
        ) : (
          <form
            className="grid gap-4"
            onSubmit={(e) => {
              e.preventDefault();
              publish.mutate();
            }}
          >
            <div className="grid gap-1.5">
              <Label htmlFor="pub-title">{t("publish.commitTitle")}</Label>
              <Input id="pub-title" value={title ?? p.title} onChange={(e) => setTitle(e.target.value)} maxLength={72} />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="pub-body">{t("publish.commitBody")}</Label>
              <Textarea id="pub-body" rows={4} value={body ?? p.body} onChange={(e) => setBody(e.target.value)} className="font-mono text-[0.8125rem]" />
            </div>
            <div className="grid gap-2 rounded-lg border p-3 text-[0.8125rem]">
              <div>
                <span className="text-muted-foreground">{t("publish.coAuthors")}</span>{" "}
                {p.co_authors.length ? p.co_authors.map((c) => c.name).join(", ") : t("publish.nobody")}
              </div>
              <div>
                <span className="text-muted-foreground">{t("publish.reviewedBy")}</span> {p.reviewers.map((c) => c.name).join(", ") || t("publish.nobody")}
              </div>
              {p.assisted && <div className="text-muted-foreground">{t("publish.assisted")}</div>}
              <div className="flex items-center gap-1.5 text-muted-foreground">
                <GitPullRequest className="size-3.5" />
                {t(repo.forge_kind === "git" ? "publish.mergeLocal" : "publish.viaPR", { branch: p.target_branch })}
              </div>
              {p.protected && <div className="text-muted-foreground">{t("publish.protected", { branch: p.target_branch })}</div>}
            </div>
            {findings.length > 0 && (
              <div className="grid gap-1.5 rounded-lg border px-3 py-2 text-[0.8125rem]">
                <p className="text-muted-foreground">{t("consistency.publishNote", { count: findings.length })}</p>
                <ul className="grid gap-1">
                  {findings.slice(0, 5).map((f) => (
                    <li key={f.id} className="flex items-start gap-2">
                      {f.kind === "contradiction" ? <Split className="mt-0.5 size-3.5 shrink-0 text-destructive" /> : <Copy className="mt-0.5 size-3.5 shrink-0 text-muted-foreground" />}
                      <span className="min-w-0">
                        <span className="font-medium">{f.a.path}</span> · {f.explanation}
                      </span>
                    </li>
                  ))}
                </ul>
              </div>
            )}
            {p.required_reviews > 0 && (
              <p className="flex items-start gap-2 rounded-lg border border-warning/40 bg-warning/5 px-3 py-2 text-[0.8125rem]">
                <TriangleAlert className="mt-0.5 size-4 shrink-0 text-warning" />
                {t("publish.requiredReviews", { count: p.required_reviews })}
              </p>
            )}
            {p.blocked && (
              <p className="flex items-start gap-2 rounded-lg border border-warning/40 bg-warning/5 px-3 py-2 text-[0.8125rem]">
                <TriangleAlert className="mt-0.5 size-4 shrink-0 text-warning" />
                {t(`publish.blocked.${p.blocked}`, { defaultValue: t("publish.blocked.other") })}
              </p>
            )}
            {publish.error && <p className="text-[0.8125rem] text-destructive">{errorMessage(publish.error, t("errors.generic"))}</p>}
            <DialogFooter>
              <Button type="button" variant="ghost" onClick={onClose}>
                {t("common.cancel")}
              </Button>
              <Button type="submit" disabled={!!p.blocked || publish.isPending || !(title ?? p.title).trim()}>
                {publish.isPending && <Loader2 className="animate-spin" />}
                {t("publish.action")}
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  );
}
