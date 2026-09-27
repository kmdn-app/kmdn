import { useEffect, useMemo, useRef, useState } from "react";
import { Link, useNavigate } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { Copy, EyeOff, Link2, Loader2, RefreshCw, Split, Wand2 } from "lucide-react";
import type { components } from "@kmdn/api-client";
import { cn } from "cn";
import { Time } from "@/components/time";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { api, errorMessage, unwrap } from "@/lib/api";
import { realtime } from "@/lib/realtime";
import type { RepoView } from "@/lib/repos";
import type { RevisionView } from "@/lib/revisions";

export type Finding = components["schemas"]["ConsistencyFinding"];
type Side = components["schemas"]["ConsistencySide"];

/** A revision's consistency findings, refreshed live while a check runs. */
export function useRevisionConsistency(rev: RevisionView | undefined) {
  const qc = useQueryClient();
  const id = rev?.id;
  const q = useQuery({
    queryKey: ["revision-consistency", id],
    queryFn: () => unwrap(api.GET("/revisions/{revision}/consistency", { params: { path: { revision: id! } } })),
    enabled: !!id,
    refetchInterval: (query) => (query.state.data?.pending ? 4000 : false),
  });
  useEffect(() => {
    if (!id) return;
    return realtime.follow("revision:" + id, (ev) => {
      if (ev.type === "consistency") void qc.invalidateQueries({ queryKey: ["revision-consistency", id] });
    });
  }, [qc, id]);
  return q;
}

/** Wraps the claim inside an excerpt of the passage in <mark>. */
export function Excerpt({ text, claim, className }: { text: string; claim?: string; className?: string }) {
  const at = claim ? text.toLowerCase().indexOf(claim.toLowerCase()) : -1;
  if (at < 0) return <p className={cn("line-clamp-5", className)}>{text}</p>;
  const start = Math.max(0, at - 160);
  const end = Math.min(text.length, at + claim!.length + 160);
  return (
    <p className={className}>
      {start > 0 && "…"}
      {text.slice(start, at)}
      <mark className="rounded-sm bg-warning/25 px-0.5 text-foreground">{text.slice(at, at + claim!.length)}</mark>
      {text.slice(at + claim!.length, end)}
      {end < text.length && "…"}
    </p>
  );
}

function SideCard({ repo, side, claim, label, revisionNumber }: { repo: RepoView; side: Side; claim?: string; label: string; revisionNumber?: number }) {
  return (
    <div className="grid min-w-0 content-start gap-1 rounded-lg bg-muted/40 p-3">
      <div className="flex min-w-0 items-baseline gap-1.5 text-[0.71875rem] text-muted-foreground">
        <span className="shrink-0 font-medium tracking-wide uppercase">{label}</span>
        {side.heading && <span className="truncate">› {side.heading}</span>}
      </div>
      <Link
        to="/$owner/$repo/$"
        params={{ owner: repo.owner, repo: repo.name, _splat: side.path }}
        search={revisionNumber ? { revision: revisionNumber } : {}}
        hash={revisionNumber ? undefined : side.slug || undefined}
        className="truncate text-[0.78125rem] font-medium hover:underline"
        title={side.path}
      >
        {side.path}
      </Link>
      <Excerpt text={side.text} claim={claim} className="text-[0.8125rem] leading-relaxed" />
    </div>
  );
}

export type FindingActions = {
  canFix: boolean;
  /** "Start a revision to fix" (the repo report) instead of "Fix". */
  startsRevision?: boolean;
  busy: boolean;
  onFix: (f: Finding, action: "fix" | "link") => void;
  onIgnore: (f: Finding) => void;
  onUnignore: (f: Finding) => void;
};

/** One contradiction or duplicate: both passages side by side, and what to do about it. */
export function FindingCard({ repo, f, actions, revisionNumber }: { repo: RepoView; f: Finding; actions: FindingActions; revisionNumber?: number }) {
  const { t } = useTranslation();
  const inRevision = f.scope !== "published";
  const open = f.status === "open";
  const fixing = f.fix && ["editing", "in_review", "approved"].includes(f.fix.state);
  return (
    <article className={cn("grid gap-3 border-b px-4 py-3.5 last:border-b-0", !open && "opacity-75")}>
      <div className="flex flex-wrap items-start gap-2">
        <span className={cn("inline-flex shrink-0 items-center gap-1 rounded-full px-2 py-0.5 text-[0.71875rem] font-medium", f.kind === "contradiction" ? "bg-destructive/10 text-destructive" : "bg-muted text-muted-foreground")}>
          {f.kind === "contradiction" ? <Split className="size-3" /> : <Copy className="size-3" />}
          {t(`consistency.${f.kind}`)}
        </span>
        <p className="min-w-0 flex-1 text-[0.84375rem]">{f.explanation}</p>
      </div>
      <div className="grid grid-cols-2 gap-2 max-md:grid-cols-1">
        <SideCard repo={repo} side={f.a} claim={f.claim_a} label={inRevision ? t("consistency.thisPage") : "A"} revisionNumber={inRevision ? revisionNumber : undefined} />
        <SideCard repo={repo} side={f.b} claim={f.claim_b} label={inRevision ? t("consistency.otherPage") : "B"} />
      </div>
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-[0.75rem] text-muted-foreground">
        <span>
          {t("consistency.firstSeen")} <Time iso={f.first_seen} />
        </span>
        <span>· {t("consistency.similar", { pct: Math.round(f.similarity * 100) })}</span>
        {f.status !== "open" && <span>· {f.status === "ignored" && f.ignore_reason ? t("consistency.ignoredBecause", { reason: f.ignore_reason }) : t(`consistency.status.${f.status}`)}</span>}
        {f.fix && (
          <span>
            ·{" "}
            <Link to="/$owner/$repo/revisions/$number" params={{ owner: repo.owner, repo: repo.name, number: String(f.fix.number) }} className="hover:underline">
              {fixing ? t("consistency.fixing", { number: f.fix.number }) : `#${f.fix.number} ${f.fix.title}`}
            </Link>
          </span>
        )}
        {actions.canFix && (
          <span className="ml-auto flex flex-wrap gap-1">
            {open && !fixing && (
              <Button size="sm" variant="ghost" className="h-7 px-2 text-[0.78125rem]" disabled={actions.busy} onClick={() => actions.onFix(f, "fix")}>
                <Wand2 />
                {actions.startsRevision ? t("consistency.startFix") : t("consistency.fix")}
              </Button>
            )}
            {open && !fixing && f.kind === "duplicate" && (
              <Button size="sm" variant="ghost" className="h-7 px-2 text-[0.78125rem]" disabled={actions.busy} onClick={() => actions.onFix(f, "link")}>
                <Link2 />
                {t("consistency.link")}
              </Button>
            )}
            {open && (
              <Button size="sm" variant="ghost" className="h-7 px-2 text-[0.78125rem]" disabled={actions.busy} onClick={() => actions.onIgnore(f)}>
                <EyeOff />
                {t("consistency.ignore")}
              </Button>
            )}
            {f.status === "ignored" && (
              <Button size="sm" variant="ghost" className="h-7 px-2 text-[0.78125rem]" disabled={actions.busy} onClick={() => actions.onUnignore(f)}>
                {t("consistency.unignore")}
              </Button>
            )}
          </span>
        )}
      </div>
    </article>
  );
}

function IgnoreDialog({ f, busy, onClose, onIgnore }: { f: Finding | null; busy: boolean; onClose: () => void; onIgnore: (reason: string) => void }) {
  const { t } = useTranslation();
  const [reason, setReason] = useState("");
  return (
    <Dialog open={!!f} onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("consistency.ignoreTitle")}</DialogTitle>
          <DialogDescription>{t("consistency.ignoreDesc")}</DialogDescription>
        </DialogHeader>
        <form
          id="ignore-finding"
          className="grid gap-1.5"
          onSubmit={(e) => {
            e.preventDefault();
            if (reason.trim()) onIgnore(reason.trim());
          }}
        >
          <Label htmlFor="ignore-reason">{t("consistency.reason")}</Label>
          <Textarea id="ignore-reason" value={reason} onChange={(e) => setReason(e.target.value)} placeholder={t("consistency.reasonPlaceholder")} autoFocus />
        </form>
        <DialogFooter>
          <Button variant="ghost" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button type="submit" form="ignore-finding" disabled={busy || !reason.trim()}>
            {busy && <Loader2 className="animate-spin" />}
            {t("consistency.ignore")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/**
 * Fix, Link instead and Ignore for a list of findings. Fixing a report
 * finding starts a revision and opens it.
 */
export function useFindingActions(repo: RepoView, invalidate: () => void, opts: { canFix: boolean; startsRevision?: boolean }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [ignoring, setIgnoring] = useState<Finding | null>(null);
  const fix = useMutation({
    mutationFn: ({ f, action }: { f: Finding; action: "fix" | "link" }) => unwrap(api.POST("/consistency/findings/{finding}/fix", { params: { path: { finding: f.id } }, body: { action } })),
    onSuccess: (d, { f }) => {
      invalidate();
      if (f.scope === "published") {
        toast.success(t("consistency.started", { number: d.revision.number }));
        void navigate({ to: "/$owner/$repo/revisions/$number", params: { owner: repo.owner, repo: repo.name, number: String(d.revision.number) } });
      } else toast.success(t("consistency.asked"));
    },
    onError: (e) => toast.error(errorMessage(e, t("errors.generic"))),
  });
  const ignore = useMutation({
    mutationFn: ({ f, reason }: { f: Finding; reason: string }) => unwrap(api.POST("/consistency/findings/{finding}/ignore", { params: { path: { finding: f.id } }, body: { reason } })),
    onSuccess: () => (setIgnoring(null), invalidate()),
    onError: (e) => toast.error(errorMessage(e, t("errors.generic"))),
  });
  const unignore = useMutation({
    mutationFn: (f: Finding) => unwrap(api.DELETE("/consistency/findings/{finding}/ignore", { params: { path: { finding: f.id } } })),
    onSuccess: invalidate,
    onError: (e) => toast.error(errorMessage(e, t("errors.generic"))),
  });
  const actions: FindingActions = {
    canFix: opts.canFix,
    startsRevision: opts.startsRevision,
    busy: fix.isPending || ignore.isPending || unignore.isPending,
    onFix: (f, action) => fix.mutate({ f, action }),
    onIgnore: setIgnoring,
    onUnignore: (f) => unignore.mutate(f),
  };
  const dialog = <IgnoreDialog key={ignoring?.id ?? "none"} f={ignoring} busy={ignore.isPending} onClose={() => setIgnoring(null)} onIgnore={(reason) => ignoring && ignore.mutate({ f: ignoring, reason })} />;
  return { actions, dialog };
}

/** The revision overview's consistency section (advisory). */
export function RevisionConsistency({ repo, rev }: { repo: RepoView; rev: RevisionView }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const q = useRevisionConsistency(rev);
  const invalidate = () => void qc.invalidateQueries({ queryKey: ["revision-consistency", rev.id] });
  const openRev = rev.state === "editing" || rev.state === "in_review" || rev.state === "approved";
  const { actions, dialog } = useFindingActions(repo, invalidate, { canFix: openRev && rev.access.can_edit });
  const run = useMutation({
    mutationFn: () => unwrap(api.POST("/revisions/{revision}/consistency", { params: { path: { revision: rev.id } } })),
    onSuccess: invalidate,
    onError: (e) => toast.error(errorMessage(e, t("errors.generic"))),
  });
  const d = q.data;
  if (!d || (!d.available && d.findings.length === 0)) return null;
  const canRun = d.available && openRev && (rev.access.member || rev.access.reviewer || rev.access.can_publish);
  return (
    <section>
      <div className="mb-3 flex items-center gap-2">
        <h2 className="text-[0.9375rem] font-semibold">{t("consistency.title")}</h2>
        {canRun && (
          <Button size="sm" variant="ghost" className="ml-auto h-7" disabled={d.pending || run.isPending} onClick={() => run.mutate()}>
            {d.pending ? <Loader2 className="animate-spin" /> : <RefreshCw />}
            {d.pending ? t("consistency.checking") : t("consistency.check")}
          </Button>
        )}
      </div>
      <div className="overflow-hidden rounded-xl border">
        {d.findings.length === 0 ? (
          <p className="px-4 py-3 text-[0.84375rem] text-muted-foreground">{d.pending ? t("consistency.checking") : t("consistency.nothing")}</p>
        ) : (
          d.findings.map((f) => <FindingCard key={f.id} repo={repo} f={f} actions={actions} revisionNumber={rev.number} />)
        )}
      </div>
      {dialog}
    </section>
  );
}

/** The first sentence of a passage (what a duplicate's underline covers). */
function lead(text: string): string {
  const s = text.split("\n")[0]!;
  const end = s.search(/[.!?](\s|$)/);
  return (end > 20 ? s.slice(0, end + 1) : s).slice(0, 160);
}

/** Underlines for a page: the revision's side of each open finding on it. */
export function useConsistencyMarks(rev: RevisionView, path: string) {
  const q = useRevisionConsistency(rev);
  return useMemo(() => {
    const findings = (q.data?.findings ?? []).filter((f) => f.status === "open");
    const marks: { id: string; kind: Finding["kind"]; quote: string }[] = [];
    for (const f of findings) {
      if (f.a.path === path) marks.push({ id: f.id, kind: f.kind, quote: f.claim_a || lead(f.a.text) });
      else if (f.b.path === path) marks.push({ id: f.id, kind: f.kind, quote: f.claim_b || lead(f.b.text) });
    }
    return { marks, findings };
  }, [q.data, path]);
}

/**
 * Shows a finding's card when the pointer rests on its underline in the
 * editor (children), with Fix, Link instead and Ignore.
 */
export function ConsistencyHover({ repo, rev, path, findings, children }: { repo: RepoView; rev: RevisionView; path: string; findings: Finding[]; children: React.ReactNode }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const wrap = useRef<HTMLDivElement>(null);
  const hideT = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const [shown, setShown] = useState<{ id: string; top: number; left: number } | null>(null);
  const openRev = rev.state === "editing" || rev.state === "in_review" || rev.state === "approved";
  const { actions, dialog } = useFindingActions(repo, () => void qc.invalidateQueries({ queryKey: ["revision-consistency", rev.id] }), { canFix: openRev && rev.access.can_edit });
  useEffect(() => () => clearTimeout(hideT.current), []);
  const f = shown ? findings.find((x) => x.id === shown.id) : undefined;
  const other = f ? (f.a.path === path ? f.b : f.a) : undefined;
  const otherClaim = f ? (f.a.path === path ? f.claim_b : f.claim_a) : undefined;
  return (
    <div
      ref={wrap}
      className="relative"
      onMouseOver={(e) => {
        const el = (e.target as HTMLElement).closest?.("[data-finding]");
        if (!el || !wrap.current) return;
        clearTimeout(hideT.current);
        const box = wrap.current.getBoundingClientRect();
        const r = el.getBoundingClientRect();
        setShown({ id: el.getAttribute("data-finding")!, top: r.bottom - box.top + 6, left: Math.max(0, Math.min(r.left - box.left, box.width - 380)) });
      }}
      onMouseOut={(e) => {
        if ((e.target as HTMLElement).closest?.("[data-finding]")) hideT.current = setTimeout(() => setShown(null), 300);
      }}
    >
      {children}
      {f && other && shown && (
        <div
          role="dialog"
          aria-label={t(`consistency.${f.kind}`)}
          className="absolute z-30 grid w-[23.125rem] max-w-[calc(100%-0.5rem)] gap-2 rounded-xl border bg-popover p-3 text-[0.8125rem] text-popover-foreground shadow-lg"
          style={{ top: shown.top, left: shown.left }}
          onMouseEnter={() => clearTimeout(hideT.current)}
          onMouseLeave={() => (hideT.current = setTimeout(() => setShown(null), 200))}
        >
          <div className="flex items-start gap-2">
            <span className={cn("inline-flex shrink-0 items-center gap-1 rounded-full px-2 py-0.5 text-[0.71875rem] font-medium", f.kind === "contradiction" ? "bg-destructive/10 text-destructive" : "bg-muted text-muted-foreground")}>
              {f.kind === "contradiction" ? <Split className="size-3" /> : <Copy className="size-3" />}
              {t(`consistency.${f.kind}`)}
            </span>
            <p className="min-w-0 flex-1">{f.explanation}</p>
          </div>
          <SideCard repo={repo} side={other} claim={otherClaim} label={t("consistency.otherPage")} />
          {actions.canFix && (
            <div className="flex flex-wrap justify-end gap-1">
              <Button size="sm" variant="ghost" className="h-7 px-2 text-[0.78125rem]" disabled={actions.busy} onClick={() => actions.onFix(f, "fix")}>
                <Wand2 />
                {t("consistency.fix")}
              </Button>
              {f.kind === "duplicate" && (
                <Button size="sm" variant="ghost" className="h-7 px-2 text-[0.78125rem]" disabled={actions.busy} onClick={() => actions.onFix(f, "link")}>
                  <Link2 />
                  {t("consistency.link")}
                </Button>
              )}
              <Button size="sm" variant="ghost" className="h-7 px-2 text-[0.78125rem]" disabled={actions.busy} onClick={() => (setShown(null), actions.onIgnore(f))}>
                <EyeOff />
                {t("consistency.ignore")}
              </Button>
            </div>
          )}
        </div>
      )}
      {dialog}
    </div>
  );
}
