import { useEffect, useRef, useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { Link } from "@tanstack/react-router";
import { CheckCheck, Eye, Loader2, MoreHorizontal, RotateCcw, ThumbsUp, Wrench } from "lucide-react";
import { Avatar } from "@/components/avatar";
import { Time } from "@/components/time";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Textarea } from "@/components/ui/textarea";
import { api, errorMessage, unwrap, useMe } from "@/lib/api";
import { useDiscussions, useThreads, type Thread } from "@/lib/threads";
import { useRepo } from "@/lib/use-repo";
import { cn } from "@/lib/utils";

export type PendingComment = { quote: string; position: { start: unknown; end: unknown } | null; prefix?: string; suffix?: string };

const REACTIONS: { kind: "+1" | "check" | "eyes"; icon: typeof ThumbsUp }[] = [
  { kind: "+1", icon: ThumbsUp },
  { kind: "check", icon: CheckCheck },
  { kind: "eyes", icon: Eye },
];

/** Replies keep line breaks; @mentions and links render as text for now. */
function Body({ text }: { text: string }) {
  return <p className="text-[13.5px] break-words whitespace-pre-wrap">{text}</p>;
}

function ThreadCard({ t, active, onFocus, onFix }: { t: Thread; active: boolean; onFocus: () => void; onFix?: () => void }) {
  const { t: tr } = useTranslation();
  const { data: me } = useMe();
  const repo = useRepo();
  const qc = useQueryClient();
  const [reply, setReply] = useState("");
  const ref = useRef<HTMLDivElement>(null);
  const refresh = () => void qc.invalidateQueries({ queryKey: t.kind === "discussion" ? ["discussions", t.repo_id] : ["threads", t.revision_id] });
  const onError = (e: unknown) => toast.error(errorMessage(e, tr("errors.generic")));
  const send = useMutation({
    mutationFn: () => unwrap(api.POST("/threads/{thread}/comments", { params: { path: { thread: t.id } }, body: { body: reply } })),
    onSuccess: () => {
      setReply("");
      refresh();
    },
    onError,
  });
  const setState = useMutation({
    mutationFn: (state: "open" | "resolved") => unwrap(api.PATCH("/threads/{thread}", { params: { path: { thread: t.id } }, body: { state } })),
    onSuccess: refresh,
    onError,
  });
  const react = useMutation({
    mutationFn: ({ id, kind, on }: { id: string; kind: "+1" | "check" | "eyes"; on: boolean }) =>
      on
        ? unwrap(api.PUT("/comments/{comment}/reactions/{kind}", { params: { path: { comment: id, kind } } }))
        : unwrap(api.DELETE("/comments/{comment}/reactions/{kind}", { params: { path: { comment: id, kind } } })),
    onSuccess: refresh,
    onError,
  });
  const del = useMutation({
    mutationFn: (id: string) => unwrap(api.DELETE("/comments/{comment}", { params: { path: { comment: id } } })),
    onSuccess: refresh,
    onError,
  });
  useEffect(() => {
    if (active) ref.current?.scrollIntoView({ block: "nearest", behavior: "smooth" });
  }, [active]);
  const replies = t.comments.length - 1;
  const quote = (t.anchor as { quote?: string }).quote;
  return (
    <div
      ref={ref}
      onClick={onFocus}
      className={cn("rounded-lg border bg-card p-3", active && "ring-2 ring-ring/40", t.state === "resolved" && "opacity-70")}
    >
      {quote && <blockquote className={cn("mb-2 line-clamp-2 border-l-2 border-warning pl-2 text-[12.5px] text-muted-foreground", t.outdated && "border-muted-foreground/40 line-through decoration-muted-foreground/40")}>{quote}</blockquote>}
      {t.outdated && <p className="mb-2 text-[11.5px] font-medium tracking-wide text-muted-foreground uppercase">{tr("comments.outdated")}</p>}
      <div className="grid gap-3">
        {t.comments.map((c) => (
          <div key={c.id} className="grid gap-1">
            <div className="flex items-center gap-2 text-[12.5px]">
              {c.author_id && <Avatar name={c.author_name ?? "?"} id={c.author_id} size="xs" />}
              <span className="font-medium">{c.author_name || tr("revision.someone")}</span>
              <span className="text-muted-foreground">
                <Time iso={c.created_at} />
                {c.edited_at && ` · ${tr("comments.edited")}`}
              </span>
              {c.author_id === me?.id && !c.deleted && (
                <DropdownMenu>
                  <DropdownMenuTrigger asChild>
                    <button type="button" className="ml-auto text-muted-foreground hover:text-foreground" aria-label={tr("comments.actions")}>
                      <MoreHorizontal className="size-3.5" />
                    </button>
                  </DropdownMenuTrigger>
                  <DropdownMenuContent align="end">
                    <DropdownMenuItem className="text-destructive" onSelect={() => del.mutate(c.id)}>
                      {tr("common.delete")}
                    </DropdownMenuItem>
                  </DropdownMenuContent>
                </DropdownMenu>
              )}
            </div>
            {c.deleted ? <p className="text-[13px] text-muted-foreground italic">{tr("comments.deleted")}</p> : <Body text={c.body} />}
            {!c.deleted && (
              <div className="flex gap-1">
                {REACTIONS.map(({ kind, icon: Icon }) => {
                  const r = c.reactions.find((x) => x.kind === kind);
                  const mine = !!r?.users.includes(me?.id ?? "");
                  return (
                    <button
                      key={kind}
                      type="button"
                      aria-label={tr(`comments.reactions.${kind}`)}
                      aria-pressed={mine}
                      onClick={(e) => {
                        e.stopPropagation();
                        react.mutate({ id: c.id, kind, on: !mine });
                      }}
                      className={cn("inline-flex h-6 items-center gap-1 rounded-full border px-1.5 text-[11.5px] text-muted-foreground hover:text-foreground", mine && "border-primary/40 bg-primary/10 text-foreground", !r && "opacity-50 hover:opacity-100")}
                    >
                      <Icon className="size-3" />
                      {r ? r.users.length : ""}
                    </button>
                  );
                })}
              </div>
            )}
          </div>
        ))}
      </div>
      {t.fix_revision && (
        <p className="mt-2 text-[12px]">
          <Link to="/$owner/$repo/revisions/$number" params={{ owner: repo.owner, repo: repo.name, number: String(t.fix_revision.number) }} className="font-medium text-primary hover:underline" onClick={(e) => e.stopPropagation()}>
            {t.fix_revision.state === "published" ? tr("comments.fixedIn", { number: t.fix_revision.number }) : tr("comments.fixingIn", { number: t.fix_revision.number })}
          </Link>
        </p>
      )}
      <div className="mt-2 flex items-center justify-between gap-1 text-[11.5px] text-muted-foreground">
        <span className="mr-auto">
          {tr("comments.activity", { count: replies })} · <Time iso={t.last_activity_at} />
        </span>
        {onFix && t.state === "open" && (!t.fix_revision || t.fix_revision.state === "closed") && (
          <Button
            variant="ghost"
            size="sm"
            className="h-6 px-2 text-[12px]"
            onClick={(e) => {
              e.stopPropagation();
              onFix();
            }}
          >
            <Wrench />
            {tr("comments.fixThis")}
          </Button>
        )}
        <Button variant="ghost" size="sm" className="h-6 px-2 text-[12px]" onClick={() => setState.mutate(t.state === "open" ? "resolved" : "open")}>
          {t.state === "open" ? <CheckCheck /> : <RotateCcw />}
          {t.state === "open" ? tr("comments.resolve") : tr("comments.reopen")}
        </Button>
      </div>
      {active && t.state === "open" && (
        <form
          className="mt-2 grid gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            send.mutate();
          }}
        >
          <Textarea rows={2} value={reply} onChange={(e) => setReply(e.target.value)} placeholder={tr("comments.replyPlaceholder")} aria-label={tr("comments.reply")} />
          <Button type="submit" size="sm" className="justify-self-end" disabled={!reply.trim() || send.isPending}>
            {send.isPending && <Loader2 className="animate-spin" />}
            {tr("comments.reply")}
          </Button>
        </form>
      )}
    </div>
  );
}

/**
 * The Comments tab: this page's threads, Hot topics or Last updated, with a
 * composer for the passage selected in the editor.
 */
export function CommentsPanel({
  revisionID,
  repoID,
  onFix,
  path,
  pending,
  onPendingDone,
  active,
  onActive,
  canComment,
  before,
}: {
  /** Shown above the threads (suggestion cards). */
  before?: React.ReactNode;
  /** A revision's threads, or (with repoID) discussions on a published page. */
  revisionID?: string;
  repoID?: string;
  /** Discussions: start a revision fixing one. */
  onFix?: (t: Thread) => void;
  path: string;
  pending: PendingComment | null;
  onPendingDone: () => void;
  active: string | null;
  onActive: (id: string | null) => void;
  canComment: boolean;
}) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [sort, setSort] = useState<"hot" | "updated">("hot");
  const [showResolved, setShowResolved] = useState(false);
  const [body, setBody] = useState("");
  const revThreads = useThreads(repoID ? undefined : revisionID, path, sort);
  const discussions = useDiscussions(repoID, path, sort);
  const threads = repoID ? discussions : revThreads;
  const create = useMutation({
    mutationFn: () => {
      const anchor = { quote: pending?.quote ?? "", ...(pending?.prefix ? { prefix: pending.prefix } : {}), ...(pending?.suffix ? { suffix: pending.suffix } : {}) };
      if (repoID) return unwrap(api.POST("/repos/{repo}/discussions", { params: { path: { repo: repoID } }, body: { path, body, anchor } }));
      return unwrap(
        api.POST("/revisions/{revision}/threads", {
          params: { path: { revision: revisionID! } },
          body: { path, body, anchor, ...(pending?.position ? { position: pending.position as Record<string, unknown> } : {}) },
        }),
      );
    },
    onSuccess: (th) => {
      setBody("");
      onPendingDone();
      onActive(th.id);
      void qc.invalidateQueries({ queryKey: repoID ? ["discussions", repoID] : ["threads", revisionID] });
    },
    onError: (e) => toast.error(errorMessage(e, t("errors.generic"))),
  });
  const list = threads.data ?? [];
  const open = list.filter((x) => x.state === "open");
  const resolved = list.filter((x) => x.state === "resolved");
  return (
    <div className="grid gap-3 p-3">
      <div className="flex items-center gap-1">
        <div role="radiogroup" aria-label={t("comments.sort")} className="inline-flex gap-0.5 rounded-md bg-muted p-0.5">
          {(["hot", "updated"] as const).map((s) => (
            <button
              key={s}
              type="button"
              role="radio"
              aria-checked={sort === s}
              onClick={() => setSort(s)}
              className={cn("h-6 rounded px-2 text-[12px] font-medium text-muted-foreground", sort === s && "bg-background text-foreground shadow-xs")}
            >
              {t(`comments.sorts.${s}`)}
            </button>
          ))}
        </div>
        <span className="ml-auto text-[12px] text-muted-foreground">{t("comments.openCount", { count: open.length })}</span>
      </div>
      {pending && canComment && (
        <form
          className="grid gap-2 rounded-lg border bg-card p-3"
          onSubmit={(e) => {
            e.preventDefault();
            create.mutate();
          }}
        >
          {pending.quote && <blockquote className="line-clamp-3 border-l-2 border-warning pl-2 text-[12.5px] text-muted-foreground">{pending.quote}</blockquote>}
          <Textarea autoFocus rows={3} value={body} onChange={(e) => setBody(e.target.value)} placeholder={t("comments.placeholder")} aria-label={t("comments.new")} />
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" size="sm" onClick={onPendingDone}>
              {t("common.cancel")}
            </Button>
            <Button type="submit" size="sm" disabled={!body.trim() || create.isPending}>
              {t("comments.comment")}
            </Button>
          </div>
        </form>
      )}
      {before}
      {!pending && list.length === 0 && !before && <p className="px-1 text-[13px] text-muted-foreground">{canComment ? t("comments.emptyHint") : t("comments.empty")}</p>}
      {open.map((th) => (
        <ThreadCard key={th.id} t={th} active={active === th.id} onFocus={() => onActive(th.id)} onFix={onFix && (() => onFix(th))} />
      ))}
      {resolved.length > 0 && (
        <button type="button" className="justify-self-start text-[12px] text-muted-foreground hover:text-foreground" onClick={() => setShowResolved((v) => !v)}>
          {showResolved ? t("comments.hideResolved") : t("comments.showResolved", { count: resolved.length })}
        </button>
      )}
      {showResolved && resolved.map((th) => <ThreadCard key={th.id} t={th} active={active === th.id} onFocus={() => onActive(th.id)} />)}
    </div>
  );
}
