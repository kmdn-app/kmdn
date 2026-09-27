import { useCallback, useEffect, useMemo, useState } from "react";
import { Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import type { Editor } from "@tiptap/react";
import { Check, ChevronRight, CloudOff, FilePen, Loader2, Lock } from "lucide-react";
import { AppShell } from "@/components/shell/app-shell";
import { TopBar } from "@/components/shell/top-bar";
import { DocView } from "@/components/doc/doc-view";
import { LinksPanel } from "@/components/links-panel";
import { PresenceStack } from "@/components/revision/presence";
import { ReviewActions } from "@/components/revision/review-actions";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { ApiError, errorMessage, useMe } from "@/lib/api";
import { RoomProvider } from "@/lib/realtime";
import { fileHref, type RepoView } from "@/lib/repos";
import { revisionRawUrl, uploadAsset, useRevision, useRevisionContent, useRevisionEvents, type RevisionView } from "@/lib/revisions";
import { cn } from "@/lib/utils";
import { EditorToolbar, PageEditor, useRoomStatus, type ImageUploader } from "./page-editor";
import { SourceEditor } from "./source-editor";

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
  const [mode, setMode] = useEditorMode();
  const onEditor = useCallback((e: Editor | null) => setEditor(e), []);
  const provider = useRoom(rev.data?.id, path);
  const status = useRoomStatus(provider);
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
    <AppShell repo={repo} revision={rev.data} currentPath={path} panel={rev.data ? { initial: "links", links: <LinksPanel repo={repo} path={path} revision={rev.data} /> } : undefined}>
      {(controls) => (
        <>
          <TopBar
            controls={controls}
            title={
              <span className="flex min-w-0 items-center gap-1.5 text-muted-foreground">
                {crumbs.slice(0, -1).map((c, i) => (
                  <span key={i} className="flex min-w-0 shrink-[2] items-center gap-1.5 max-md:hidden">
                    <span className="truncate">{c}</span>
                    <ChevronRight className="size-3 shrink-0 opacity-60" />
                  </span>
                ))}
                <span className="min-w-[3ch] max-w-full shrink-0 truncate text-foreground">{crumbs[crumbs.length - 1]}</span>
              </span>
            }
            actions={
              <>
                {rev.data && <PresenceStack repo={repo} rev={rev.data} path={path} />}
                {status && !status.error && <ModeToggle mode={mode} onChange={setMode} />}
                {rev.data && <RevisionPill rev={rev.data} />}
                {rev.data?.access.can_review && <ReviewActions repo={repo} rev={rev.data} compact />}
                <SaveState status={status} />
                <Button asChild size="sm" variant="outline">
                  <Link to="/$owner/$repo/$" params={{ owner: repo.owner, repo: repo.name, _splat: path }}>
                    {t("revision.done")}
                  </Link>
                </Button>
              </>
            }
          />
          {status?.mode === "rw" && editor && mode === "visual" && (
            <div className="flex shrink-0 overflow-x-auto border-b px-3 py-1">
              <EditorToolbar editor={editor} upload={upload} />
            </div>
          )}
          <div className="min-h-0 flex-1 overflow-auto">
            {rev.data && provider && status ? <Body repo={repo} rev={rev.data} path={path} provider={provider} onEditor={onEditor} upload={upload} mode={mode} /> : <Loading />}
          </div>
        </>
      )}
    </AppShell>
  );
}

type EditorMode = "visual" | "source";
const MODE_KEY = "kmdn-editor-mode";

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
    <div role="radiogroup" aria-label={t("editor.mode")} className="inline-flex shrink-0 gap-0.5 rounded-md bg-muted p-0.5 max-sm:hidden">
      {(["visual", "source"] as const).map((m) => (
        <button
          key={m}
          type="button"
          role="radio"
          aria-checked={mode === m}
          onClick={() => onChange(m)}
          className={cn("h-6 rounded px-2 text-[12px] font-medium text-muted-foreground", mode === m && "bg-background text-foreground shadow-xs")}
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
}: {
  repo: RepoView;
  rev: RevisionView;
  path: string;
  provider: RoomProvider;
  onEditor: (e: Editor | null) => void;
  upload?: ImageUploader;
  mode: EditorMode;
}) {
  const { t } = useTranslation();
  const { data: me } = useMe();
  const status = useRoomStatus(provider);
  const noDoc = status?.error?.code === "no_document";
  // Read-only callers get no room until someone edits: show the page as it is in the revision.
  const content = useRevisionContent(rev, path, noDoc || status?.error?.code === "not_found");
  const resolveImage = useMemo(() => (src: string) => (/^[a-z]+:|^\/\//i.test(src) ? src : revisionRawUrl(rev.id, resolveFrom(path, src))), [rev.id, path]);
  const ctx = useMemo(() => ({ path, pageHref: (p: string) => fileHref(repo, p), imageSrc: (p: string) => revisionRawUrl(rev.id, p) }), [repo, rev.id, path]);

  if (status?.error && !noDoc) {
    return (
      <div className="doc">
        <h1>{t("revision.unavailable")}</h1>
        <p className="text-muted-foreground">{status.error.message}</p>
      </div>
    );
  }
  return (
    <>
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
            <SourceEditor provider={provider} revisionID={rev.id} path={path} editable={status.mode === "rw"} />
          ) : (
            <Loading />
          )
        ) : (
          <PageEditor provider={provider} user={{ id: me.id, name: me.name }} resolveImage={resolveImage} upload={upload} onEditor={onEditor} docCtx={ctx} />
        ))
      )}
    </>
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
    <div className="mx-auto mt-5 flex max-w-[720px] items-start gap-3 rounded-xl border bg-muted/40 px-4 py-3 text-[13.5px] max-md:mx-4">
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
    <span className="inline-flex h-7 max-w-[240px] shrink-0 items-center gap-1.5 rounded-full border px-2.5 text-[12.5px] font-medium whitespace-nowrap max-sm:hidden" title={rev.title}>
      <FilePen className="size-3.5 text-muted-foreground" />
      <span className={cn("size-1.5 shrink-0 rounded-full", STATE_DOT[rev.state])} />
      <span className="truncate">
        #{rev.number} {rev.title}
      </span>
      <span className="sr-only">{t(`revision.state.${rev.state}`)}</span>
    </span>
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
    <span role="status" className="inline-flex shrink-0 items-center gap-1 text-[12.5px] text-muted-foreground max-md:hidden">
      {icon}
      {label}
    </span>
  );
}
