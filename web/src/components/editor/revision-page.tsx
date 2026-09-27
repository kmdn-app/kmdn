import { useCallback, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import type { Editor } from "@tiptap/react";
import { Check, ChevronRight, CloudOff, FilePen, GitCommitHorizontal, Loader2, Lock, MessageSquarePlus, PenLine, TriangleAlert } from "lucide-react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { AppShell } from "@/components/shell/app-shell";
import { TopBar } from "@/components/shell/top-bar";
import { useInitialPanelTab } from "@/components/shell/right-panel";
import { DocView } from "@/components/doc/doc-view";
import { LinksPanel } from "@/components/links-panel";
import { PresenceStack } from "@/components/revision/presence";
import { ReviewActions } from "@/components/revision/review-actions";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { ApiError, api, errorMessage, unwrap, useMe } from "@/lib/api";
import { RoomProvider } from "@/lib/realtime";
import { fileHref, type RepoView } from "@/lib/repos";
import { revisionRawUrl, uploadAsset, useRevision, useRevisionContent, useRevisionDiff, useRevisionEvents, useRevisionFiles, useSaveRevision, type RevisionView } from "@/lib/revisions";
import { CONTENT, listSuggestions, parse, readDoc } from "@kmdn/doc-engine";
import { ChangesView, SourceDiff } from "@/components/revision/diff-views";
import { cn } from "@/lib/utils";
import { EditorToolbar, PageEditor, useRoomStatus, type ImageUploader } from "./page-editor";
import { SourceEditor } from "./source-editor";
import { selectionAnchor } from "./comment-anchors";
import { CommentsPanel, type PendingComment } from "@/components/revision/comments-panel";
import { useThreads } from "@/lib/threads";
import { SuggestionList } from "@/components/revision/suggestions-panel";
import { UpdatesBanner } from "@/components/revision/updates";
import { ConsistencyHover, useConsistencyMarks } from "@/components/consistency/findings";
import { openPanel, useIsPhone } from "@/lib/media";

/** Opens the page's room for as long as the view shows it. */
function useRoom(revisionID: string | undefined, path: string) {
  const [provider, setProvider] = useState<RoomProvider | null>(null);
  useEffect(() => {
    if (!revisionID) return;
    const p = new RoomProvider(revisionID, path);
    // An external resource with its own lifecycle: created and destroyed with the effect.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setProvider(p);
    return () => {
      p.destroy();
      setProvider(null);
    };
  }, [revisionID, path]);
  return provider;
}

export function RevisionPage({ repo, path, number }: { repo: RepoView; path: string; number: number }) {
  const { t } = useTranslation();
  const rev = useRevision(repo, number);
  useRevisionEvents(repo, rev.data);
  const crumbs = path.split("/");
  const [editor, setEditor] = useState<Editor | null>(null);
  const phone = useIsPhone();
  const [storedMode, setMode] = useEditorMode();
  // Phones read and review; editing (and source mode) needs a bigger screen.
  const mode: EditorMode = phone ? "visual" : storedMode;
  const [view, setView] = useState<ReviewView>("result");
  const [suggesting, setSuggesting] = useSuggesting();
  const [pending, setPending] = useState<PendingComment | null>(null);
  const [activeThread, setActiveThread] = useState<string | null>(null);
  const threadsQ = useThreads(rev.data?.id, path, "hot");
  const resolvedSet = useMemo(() => new Set((threadsQ.data ?? []).filter((x) => x.state === "resolved").map((x) => x.id)), [threadsQ.data]);
  const comments = useMemo(() => ({ active: activeThread, hidden: resolvedSet, onClick: (id: string) => (setActiveThread(id), openPanel()) }), [activeThread, resolvedSet]);
  const files = useRevisionFiles(rev.data);
  const inRevision = !!files.data?.some((f) => f.path === path && f.op !== "delete");
  const onEditor = useCallback((e: Editor | null) => setEditor(e), []);
  const provider = useRoom(rev.data?.id, path);
  const status = useRoomStatus(provider);
  const suggestionCount = usePendingSuggestions(provider);
  const openThreads = (threadsQ.data ?? []).filter((x) => x.state === "open").length;
  const initialTab = useInitialPanelTab(path, threadsQ.isSuccess && (!provider || !!status?.synced || !!status?.error), openThreads + suggestionCount > 0);
  const revID = rev.data?.id;
  const upload = useMemo(
    () =>
      revID
        ? async (f: File) => {
            const r = await uploadAsset(revID, path, f);
            return { src: r.src, alt: r.markdown.slice(2, r.markdown.indexOf("]")) };
          }
        : undefined,
    [revID, path],
  );

  if (rev.error) {
    return (
      <AppShell repo={repo} currentPath={path}>
        {() => (
          <div className="doc">
            <h1>{t("revision.notFound")}</h1>
            <p className="text-muted-foreground">{errorMessage(rev.error, t("errors.generic"))}</p>
          </div>
        )}
      </AppShell>
    );
  }
  return (
    <AppShell
      repo={repo}
      revision={rev.data}
      currentPath={path}
      panel={
        rev.data
          ? {
              initial: pending || activeThread ? "comments" : initialTab,
              links: <LinksPanel repo={repo} path={path} revision={rev.data} />,
              comments: (
                <CommentsPanel
                  revisionID={rev.data.id}
                  path={path}
                  pending={pending}
                  onPendingDone={() => setPending(null)}
                  active={activeThread}
                  onActive={setActiveThread}
                  canComment={rev.data.state !== "published" && rev.data.state !== "closed"}
                  before={mode === "visual" ? <SuggestionList editor={editor} rev={rev.data} path={path} /> : undefined}
                />
              ),
            }
          : undefined
      }
    >
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
                <span className="min-w-[3ch] max-w-full shrink-0 truncate text-foreground">{crumbs[crumbs.length - 1]}</span>
              </span>
            }
            actions={
              <>
                {rev.data && !phone && <PresenceStack repo={repo} rev={rev.data} path={path} />}
                {editor && view === "result" && mode === "visual" && rev.data?.state !== "published" && rev.data?.state !== "closed" && (
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={() => {
                      const anchor = selectionAnchor(editor);
                      setPending({ quote: anchor?.quote ?? "", position: anchor?.position ?? null });
                      if (!controls.panelOpen) controls.togglePanel();
                    }}
                    title={t("comments.commentOnSelection")}
                  >
                    <MessageSquarePlus />
                    <span className="@max-5xl:sr-only">{t("comments.comment")}</span>
                  </Button>
                )}
                {editor && !phone && status?.mode === "rw" && view === "result" && mode === "visual" && (
                  <Button
                    size="sm"
                    variant={suggesting ? "secondary" : "ghost"}
                    aria-pressed={suggesting}
                    onClick={() => setSuggesting(!suggesting)}
                    title={t("suggestions.toggleHint")}
                    className={cn(suggesting && "text-success")}
                  >
                    <PenLine />
                    <span className="@max-5xl:sr-only">{t("suggestions.toggle")}</span>
                  </Button>
                )}
                {inRevision && !phone && <ViewToggle view={view} onChange={setView} />}
                {status && !status.error && view === "result" && !phone && <ModeToggle mode={mode} onChange={setMode} />}
                {rev.data && <RevisionPill rev={rev.data} />}
                {rev.data?.access.can_review && !phone && <ReviewActions repo={repo} rev={rev.data} compact />}
                {!phone && <SaveState status={status} />}
                {rev.data?.access.can_edit && !phone && <SaveAllButton repo={repo} rev={rev.data} />}
              </>
            }
          />
          {!phone && status?.mode === "rw" && editor && mode === "visual" && view === "result" && (
            <div className="flex shrink-0 overflow-x-auto border-b px-3 py-1">
              <EditorToolbar editor={editor} upload={upload} />
            </div>
          )}
          {phone && inRevision && (
            <div className="flex shrink-0 items-center gap-2 border-b px-3 py-1.5">
              <span className="text-[0.75rem] text-muted-foreground">{t("revision.phoneReadOnly")}</span>
              <span className="flex-1" />
              <ViewToggle view={view === "source" ? "changes" : view} onChange={setView} views={["result", "changes"]} />
            </div>
          )}
          <div className="min-h-0 flex-1 overflow-auto">
            {rev.data && provider && status ? <Body repo={repo} rev={rev.data} path={path} provider={provider} onEditor={onEditor} upload={upload} mode={mode} view={inRevision ? (phone && view === "source" ? "changes" : view) : "result"} comments={comments} suggesting={suggesting && status.mode === "rw" && !phone} readOnly={phone} /> : <Loading />}
          </div>
          {phone && rev.data && (rev.data.access.can_review || rev.data.access.can_submit || rev.data.access.can_publish) && (
            <div className="flex shrink-0 justify-end gap-2 border-t bg-background px-3 py-2 pb-[max(0.5rem,env(safe-area-inset-bottom))] [&>*]:flex-1">
              <ReviewActions repo={repo} rev={rev.data} />
            </div>
          )}
        </>
      )}
    </AppShell>
  );
}

type EditorMode = "visual" | "source";
type ReviewView = "result" | "changes" | "source";

/** Result (the editor, with changes marked), Changes (rendered diff), Source diff. */
function ViewToggle({ view, onChange, views }: { view: ReviewView; onChange: (v: ReviewView) => void; views?: ReviewView[] }) {
  const { t } = useTranslation();
  return (
    <div role="radiogroup" aria-label={t("diff.view")} className={cn("inline-flex shrink-0 gap-0.5 rounded-md bg-muted p-0.5", !views && "@max-xl:hidden")}>
      {(views ?? (["result", "changes", "source"] as const)).map((v) => (
        <button
          key={v}
          type="button"
          role="radio"
          aria-checked={view === v}
          onClick={() => onChange(v)}
          className={cn("h-6 rounded px-2 text-[0.75rem] font-medium text-muted-foreground", view === v && "bg-background text-foreground shadow-xs")}
        >
          {t(`diff.views.${v}`)}
        </button>
      ))}
    </div>
  );
}
const MODE_KEY = "kmdn-editor-mode";
const SUGGESTING_KEY = "kmdn-suggesting";

/** The Suggesting toggle, remembered per browser. */
function useSuggesting(): [boolean, (v: boolean) => void] {
  const [on, setOn] = useState(() => {
    try {
      return localStorage.getItem(SUGGESTING_KEY) === "1";
    } catch {
      return false;
    }
  });
  const set = (v: boolean) => {
    setOn(v);
    try {
      localStorage.setItem(SUGGESTING_KEY, v ? "1" : "0");
    } catch {
      /* storage unavailable */
    }
  };
  return [on, set];
}

/** How many suggestions the room's document holds (source mode, where there's no editor to ask). */
function usePendingSuggestions(provider: RoomProvider | null): number {
  const [n, setN] = useState(0);
  useEffect(() => {
    if (!provider) return;
    const frag = provider.doc.getXmlFragment(CONTENT);
    let timer: ReturnType<typeof setTimeout> | undefined;
    const count = () => setN(listSuggestions(readDoc(frag)).length);
    const later = () => {
      clearTimeout(timer);
      timer = setTimeout(count, 250);
    };
    count();
    frag.observeDeep(later);
    return () => {
      clearTimeout(timer);
      frag.unobserveDeep(later);
    };
  }, [provider]);
  return provider ? n : 0;
}

/** Visual or source editing, remembered per browser. */
function useEditorMode(): [EditorMode, (m: EditorMode) => void] {
  const [mode, setMode] = useState<EditorMode>(() => {
    try {
      return localStorage.getItem(MODE_KEY) === "source" ? "source" : "visual";
    } catch {
      return "visual";
    }
  });
  const set = (m: EditorMode) => {
    setMode(m);
    try {
      localStorage.setItem(MODE_KEY, m);
    } catch {
      /* storage unavailable */
    }
  };
  return [mode, set];
}

function ModeToggle({ mode, onChange }: { mode: EditorMode; onChange: (m: EditorMode) => void }) {
  const { t } = useTranslation();
  return (
    <div role="radiogroup" aria-label={t("editor.mode")} className="inline-flex shrink-0 gap-0.5 rounded-md bg-muted p-0.5 @max-xl:hidden">
      {(["visual", "source"] as const).map((m) => (
        <button
          key={m}
          type="button"
          role="radio"
          aria-checked={mode === m}
          onClick={() => onChange(m)}
          className={cn("h-6 rounded px-2 text-[0.75rem] font-medium text-muted-foreground", mode === m && "bg-background text-foreground shadow-xs")}
        >
          {t(`editor.modes.${m}`)}
        </button>
      ))}
    </div>
  );
}

function Loading() {
  return (
    <div className="doc grid gap-3">
      <Skeleton className="h-9 w-2/3" />
      <Skeleton className="h-4 w-full" />
      <Skeleton className="h-4 w-5/6" />
    </div>
  );
}

function Body({
  repo,
  rev,
  path,
  provider,
  onEditor,
  upload,
  mode,
  view,
  comments,
  suggesting,
  readOnly,
}: {
  repo: RepoView;
  rev: RevisionView;
  path: string;
  provider: RoomProvider;
  onEditor: (e: Editor | null) => void;
  upload?: ImageUploader;
  mode: EditorMode;
  view: ReviewView;
  comments: { active: string | null; hidden: Set<string>; onClick: (id: string) => void };
  suggesting: boolean;
  readOnly?: boolean;
}) {
  const { t } = useTranslation();
  const { data: me } = useMe();
  // Source mode can't record suggestions: it's read-only while some are pending.
  const pendingSuggestions = usePendingSuggestions(mode === "source" ? provider : null);
  const status = useRoomStatus(provider);
  const noDoc = status?.error?.code === "no_document";
  // Read-only callers get no room until someone edits: show the page as it is in the revision.
  const content = useRevisionContent(rev, path);
  const diff = useRevisionDiff(rev, path, view === "source");
  const inRev = content.data?.in_revision ?? false;
  const baseMD = content.data?.base ?? "";
  const baseDoc = useMemo(() => (inRev && baseMD ? parse(baseMD).doc : null), [inRev, baseMD]);
  const resolveImage = useMemo(() => (src: string) => (/^[a-z]+:|^\/\//i.test(src) ? src : revisionRawUrl(rev.id, resolveFrom(path, src))), [rev.id, path]);
  const ctx = useMemo(() => ({ path, pageHref: (p: string) => fileHref(repo, p), imageSrc: (p: string) => revisionRawUrl(rev.id, p) }), [repo, rev.id, path]);
  const consistency = useConsistencyMarks(rev, path);

  if (status?.error && !noDoc) {
    return (
      <div className="doc">
        <h1>{t("revision.unavailable")}</h1>
        <p className="text-muted-foreground">{status.error.message}</p>
      </div>
    );
  }
  if (view === "changes") {
    return content.data ? <ChangesView base={content.data.base} content={content.data.content} ctx={ctx} /> : <Loading />;
  }
  if (view === "source") {
    return diff.data ? <SourceDiff hunks={diff.data.hunks} /> : <Loading />;
  }
  return (
    <>
      <UpdatesBanner rev={rev} className="mx-auto mt-5 max-w-[45rem] max-md:mx-4" />
      <PageConflict rev={rev} path={path} />
      <ReadOnlyBanner rev={rev} reason={status?.mode === "ro" ? status.reason : undefined} />
      {noDoc ? (
        content.data ? (
          <DocView markdown={content.data.content} ctx={ctx} />
        ) : content.error ? (
          <div className="doc">
            <p className="text-muted-foreground">{content.error instanceof ApiError && content.error.status === 404 ? t("file.missing") : errorMessage(content.error, t("errors.generic"))}</p>
          </div>
        ) : (
          <Loading />
        )
      ) : (
        me &&
        (mode === "source" ? (
          status?.synced ? (
            <>
              {pendingSuggestions > 0 && (
                <div className="mx-auto mt-5 flex max-w-[45rem] items-start gap-3 rounded-xl border bg-muted/40 px-4 py-3 text-[0.84375rem] max-md:mx-4">
                  <Lock className="mt-0.5 size-4 text-muted-foreground" />
                  <div>{t("suggestions.sourceLocked", { count: pendingSuggestions })}</div>
                </div>
              )}
              <SourceEditor provider={provider} revisionID={rev.id} path={path} editable={status.mode === "rw" && pendingSuggestions === 0} />
            </>
          ) : (
            <Loading />
          )
        ) : (
          <ConsistencyHover repo={repo} rev={rev} path={path} findings={consistency.findings}>
            <PageEditor provider={provider} user={{ id: me.id, name: me.name }} resolveImage={resolveImage} upload={upload} onEditor={onEditor} docCtx={ctx} base={baseDoc} comments={comments} consistency={consistency.marks} suggesting={suggesting} readOnly={readOnly} />
          </ConsistencyHover>
        ))
      )}
    </>
  );
}

/** Published deleted this page while the revision changes it: keep it or let it go. */
function PageConflict({ rev, path }: { rev: RevisionView; path: string }) {
  const { t } = useTranslation();
  const files = useRevisionFiles(rev);
  const qc = useQueryClient();
  const resolve = useMutation({
    mutationFn: (choice: "keep" | "delete") => unwrap(api.POST("/revisions/{revision}/conflicts/resolve", { params: { path: { revision: rev.id } }, body: { path, choice } })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["revision-files", rev.id] });
      void qc.invalidateQueries({ queryKey: ["revision"] });
    },
    onError: (e) => toast.error(errorMessage(e, t("errors.generic"))),
  });
  const f = files.data?.find((x) => x.path === path);
  if (f?.conflict !== "deleted_upstream") return null;
  return (
    <div className="mx-auto mt-5 flex max-w-[45rem] flex-wrap items-center gap-3 rounded-xl border border-destructive/40 bg-destructive/5 px-4 py-3 text-[0.84375rem] max-md:mx-4">
      <TriangleAlert className="size-4 shrink-0 text-destructive" />
      <span className="min-w-0 flex-1">{t("updates.conflict.pageDeleted")}</span>
      {rev.access.can_edit && (
        <span className="flex gap-2">
          <Button size="sm" variant="outline" disabled={resolve.isPending} onClick={() => resolve.mutate("keep")}>
            {t("updates.conflict.keepPage")}
          </Button>
          <Button size="sm" variant="ghost" disabled={resolve.isPending} onClick={() => resolve.mutate("delete")}>
            {t("updates.conflict.deletePage")}
          </Button>
        </span>
      )}
    </div>
  );
}

function resolveFrom(page: string, src: string) {
  const dir = page.split("/").slice(0, -1);
  for (const part of src.split("/")) {
    if (part === "..") dir.pop();
    else if (part !== "." && part !== "") dir.push(part);
  }
  return dir.join("/");
}

function ReadOnlyBanner({ rev, reason }: { rev: RevisionView; reason?: string }) {
  const { t } = useTranslation();
  if (!reason) return null;
  const editors = rev.members.map((m) => m.name.split(" ")[0]).join(", ");
  return (
    <div className="mx-auto mt-5 flex max-w-[45rem] items-start gap-3 rounded-xl border bg-muted/40 px-4 py-3 text-[0.84375rem] max-md:mx-4">
      <Lock className="mt-0.5 size-4 text-muted-foreground" />
      <div>{t(`revision.readOnly.${reason}`, { defaultValue: t("revision.readOnly.generic"), editors })}</div>
    </div>
  );
}

const STATE_DOT: Record<string, string> = {
  editing: "bg-sky-500",
  in_review: "bg-warning",
  approved: "bg-success",
  publishing: "bg-success",
  published: "bg-muted-foreground",
  closed: "bg-muted-foreground",
};

function RevisionPill({ rev }: { rev: RevisionView }) {
  const { t } = useTranslation();
  return (
    <span className="inline-flex h-7 max-w-[15rem] shrink-0 items-center gap-1.5 rounded-full border px-2.5 text-[0.78125rem] font-medium whitespace-nowrap @max-6xl:hidden" title={rev.title}>
      <FilePen className="size-3.5 text-muted-foreground" />
      <span className={cn("size-1.5 shrink-0 rounded-full", STATE_DOT[rev.state])} />
      <span className="truncate">
        #{rev.number} {rev.title}
      </span>
      <span className="sr-only">{t(`revision.state.${rev.state}`)}</span>
    </span>
  );
}

/**
 * Save all: one commit on the revision's branch (docs/specs/05-collaboration.md#saving-and-checkpoints).
 * A dot marks content that differs from the last commit.
 */
function SaveAllButton({ repo, rev }: { repo: RepoView; rev: RevisionView }) {
  const { t } = useTranslation();
  const save = useSaveRevision(repo, rev);
  const unsaved = rev.unsaved_changes;
  return (
    <Button
      size="sm"
      variant="outline"
      disabled={save.isPending}
      title={unsaved ? t("revision.unsavedHint") : t("revision.allSaved")}
      onClick={() =>
        save.mutate(undefined, {
          onSuccess: (cp) => (cp ? toast.success(t("revision.savedAs", { sha: cp.commit_sha?.slice(0, 7) ?? "" })) : toast(t("revision.allSaved"))),
          onError: (e) => toast.error(errorMessage(e, t("errors.generic"))),
        })
      }
    >
      {save.isPending ? <Loader2 className="animate-spin" /> : <GitCommitHorizontal />}
      {t("revision.saveAll")}
      {unsaved && !save.isPending && (
        <>
          <span aria-hidden className="size-1.5 rounded-full bg-warning" />
          <span className="sr-only">{t("revision.unsaved")}</span>
        </>
      )}
    </Button>
  );
}

function SaveState({ status }: { status: ReturnType<typeof useRoomStatus> }) {
  const { t } = useTranslation();
  if (!status) return null;
  let icon = <Check className="size-3.5" />;
  let label = t("revision.saved");
  if (!status.online) {
    icon = <CloudOff className="size-3.5" />;
    label = t("revision.offline");
  } else if (!status.synced) {
    icon = <Loader2 className="size-3.5 animate-spin" />;
    label = t("revision.connecting");
  } else if (status.mode === "ro") {
    icon = <Lock className="size-3.5" />;
    label = t("revision.viewOnly");
  }
  return (
    <span role="status" title={label} className="inline-flex shrink-0 items-center gap-1 text-[0.78125rem] text-muted-foreground @max-3xl:hidden">
      {icon}
      <span className="@max-5xl:sr-only">{label}</span>
    </span>
  );
}
