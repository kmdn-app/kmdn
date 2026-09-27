import { useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { ArrowUp, FilePen, Loader2, Plus, Sparkles, Wrench } from "lucide-react";
import type { components } from "@kmdn/api-client";
import { DocView } from "@/components/doc/doc-view";
import { Button } from "@/components/ui/button";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { api, errorMessage, unwrap, useAssistantStatus, useMe } from "@/lib/api";
import { realtime } from "@/lib/realtime";
import { openPanel } from "@/lib/media";
import { atLeast, fileHref, rawUrl, type RepoView } from "@/lib/repos";
import type { RevisionView } from "@/lib/revisions";
import { cn } from "@/lib/utils";

type AssistantMessage = components["schemas"]["AssistantMessage"];

/** [[path#slug]] citations become links (DocView resolves them from the repo root). */
export function withCitations(text: string): string {
  return text.replace(/\[\[([^\]\s#]+)(?:#([^\]\s]+))?\]\]/g, (_m, p: string, slug?: string) => {
    const page = p.split("/").pop()!.replace(/\.mdx?$/, "");
    return `[${slug ? `${page} › ${slug.replace(/-/g, " ")}` : page}](${p}${slug ? `#${slug}` : ""})`;
  });
}


/** Streams a thread: live text and tool lines while a run goes, whether a prompt waits for the next run, refetching when messages land. */
function useThreadStream(threadID: string | null) {
  const qc = useQueryClient();
  const [live, setLive] = useState<{ text: string; tools: string[]; running: boolean; queued: boolean }>({ text: "", tools: [], running: false, queued: false });
  useEffect(() => {
    if (!threadID) return;
    return realtime.follow("assistant:" + threadID, (ev) => {
      switch (ev.kind) {
        case "running":
          setLive({ text: "", tools: [], running: true, queued: false });
          break;
        case "queued":
          setLive((l) => ({ ...l, queued: true }));
          break;
        case "delta":
          setLive((l) => ({ ...l, running: true, text: l.text + String(ev.text ?? "") }));
          break;
        case "tool":
          setLive((l) => ({ ...l, running: true, text: "", tools: [...l.tools, String(ev.label ?? ev.name)] }));
          break;
        case "message":
          setLive((l) => ({ ...l, text: "" }));
          void qc.invalidateQueries({ queryKey: ["assistant-thread", threadID] });
          void qc.invalidateQueries({ queryKey: ["revision-assistant"] });
          break;
        case "done":
        case "error":
          setLive((l) => ({ text: "", tools: [], running: false, queued: l.queued }));
          void qc.invalidateQueries({ queryKey: ["assistant-thread", threadID] });
          void qc.invalidateQueries({ queryKey: ["revision-assistant"] });
          break;
      }
    });
  }, [qc, threadID]);
  return live;
}

function MessageItem({ m, repo, revision, canStart, onAccept, accepting }: { m: AssistantMessage; repo: RepoView; revision?: RevisionView; canStart: boolean; onAccept: (callID: string) => void; accepting: boolean }) {
  const { t } = useTranslation();
  const ctx = useMemo(() => ({ path: "_.md", pageHref: (p: string) => fileHref(repo, p), imageSrc: (p: string) => rawUrl(repo, p) }), [repo]);
  if (m.role === "user") {
    return (
      <div className="grid justify-items-end gap-1">
        {m.author_name && <span className="text-[11.5px] text-muted-foreground">{m.author_name}</span>}
        <div className="max-w-[90%] rounded-2xl rounded-tr-sm bg-muted px-3 py-2 text-[13.5px] whitespace-pre-wrap">{m.parts.map((p) => p.text).join("")}</div>
      </div>
    );
  }
  return (
    <div className="grid gap-1.5">
      {m.parts.map((p, i) => {
        if (p.type === "tool")
          return (
            <div key={i} className="flex items-center gap-1.5 text-[12px] text-muted-foreground">
              <Wrench className="size-3" />
              {p.label}
            </div>
          );
        if (p.type === "edit")
          return (
            <Link
              key={i}
              to="/$owner/$repo/$"
              params={{ owner: repo.owner, repo: repo.name, _splat: p.path ?? "" }}
              search={revision ? { revision: revision.number } : {}}
              className="flex items-center gap-1.5 justify-self-start rounded-md border px-2 py-1 text-[12px] hover:bg-accent"
            >
              <FilePen className="size-3.5 text-muted-foreground" />
              {p.label}
            </Link>
          );
        if (p.type === "file_op")
          return (
            <div key={i} className="grid gap-2 rounded-xl border bg-card p-3 text-[13px]">
              <span className="font-medium">{p.label}</span>
              {p.accepted ? (
                <span className="text-[12px] text-success">{t("assistant.done")}</span>
              ) : (
                canStart && (
                  <Button size="sm" variant="outline" className="justify-self-start" disabled={accepting} onClick={() => onAccept(p.call_id!)}>
                    {accepting && <Loader2 className="animate-spin" />}
                    {t("assistant.confirm")}
                  </Button>
                )
              )}
            </div>
          );
        if (p.type === "proposal" && p.proposal)
          return (
            <div key={i} className="grid gap-2 rounded-xl border bg-card p-3">
              <div className="flex items-center gap-2 text-[13px] font-medium">
                <FilePen className="size-4 text-muted-foreground" />
                {t("assistant.startRevision", { title: p.proposal.title })}
              </div>
              <p className="text-[12.5px] text-muted-foreground">{p.proposal.description}</p>
              <div className="flex flex-wrap gap-1">
                {p.proposal.files.map((f) => (
                  <span key={f} className="rounded bg-muted px-1.5 py-0.5 font-mono text-[11px]">
                    {f}
                  </span>
                ))}
              </div>
              {canStart && !p.accepted && (
                <Button size="sm" className="justify-self-start" disabled={accepting} onClick={() => onAccept(p.call_id!)}>
                  {accepting && <Loader2 className="animate-spin" />}
                  {t("assistant.start")}
                </Button>
              )}
              <p className="text-[11.5px] text-muted-foreground">{t("assistant.movesNotice")}</p>
            </div>
          );
        return (
          <div key={i} className="assistant-text">
            <DocView markdown={withCitations(p.text ?? "")} ctx={ctx} className="!max-w-none !p-0" />
          </div>
        );
      })}
    </div>
  );
}

/**
 * The Assistant tab: private Q&A about the repository's published content
 * (docs/specs/08-assistant.md#surfaces). Answers cite pages; asking for a
 * change proposes a revision.
 */
const THREAD_EVENT = "kmdn:assistant-thread";

/** Opens the right panel on a Q&A thread, e.g. one just started from the repo home. */
export function showAssistantThread(id: string) {
  window.dispatchEvent(new CustomEvent<string>(THREAD_EVENT, { detail: id }));
  openPanel();
}

export function AssistantPanel({ repo, path, revision }: { repo: RepoView; path?: string; revision?: RevisionView }) {
  const { t } = useTranslation();
  const { data: me } = useMe();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const status = useAssistantStatus();
  const threads = useQuery({
    queryKey: ["assistant-threads", repo.id],
    queryFn: async () => (await unwrap(api.GET("/repos/{repo}/assistant/threads", { params: { path: { repo: repo.id } } }))).items,
    enabled: !!status.data?.enabled && !revision,
  });
  // In a revision: its one shared thread.
  const shared = useQuery({
    queryKey: ["revision-assistant", revision?.id],
    queryFn: () => unwrap(api.GET("/revisions/{revision}/assistant", { params: { path: { revision: revision!.id } } })),
    enabled: !!revision,
  });
  const [threadID, setThreadID] = useState<string | null>(null);
  useEffect(() => {
    const show = (e: Event) => setThreadID((e as CustomEvent<string>).detail);
    window.addEventListener(THREAD_EVENT, show);
    return () => window.removeEventListener(THREAD_EVENT, show);
  }, []);
  const current = revision ? (shared.data?.thread.id ?? null) : threadID === "new" ? null : (threadID ?? threads.data?.[0]?.id ?? null);
  const qa = useQuery({
    queryKey: ["assistant-thread", current],
    queryFn: () => unwrap(api.GET("/assistant/threads/{thread}", { params: { path: { thread: current! } } })),
    enabled: !!current && !revision,
  });
  const thread = revision ? shared : qa;
  const canPrompt = revision ? !!shared.data?.can_prompt : true;
  const live = useThreadStream(current);
  const [text, setText] = useState("");
  const endRef = useRef<HTMLDivElement>(null);
  const send = useMutation({
    mutationFn: async () => {
      const selection = window.getSelection()?.toString().slice(0, 2000) || undefined;
      const context = { path, selection };
      if (!current) {
        const r = await unwrap(api.POST("/repos/{repo}/assistant/threads", { params: { path: { repo: repo.id } }, body: { text, context } }));
        setThreadID(r.thread.id);
        void qc.invalidateQueries({ queryKey: ["assistant-threads", repo.id] });
        return;
      }
      await unwrap(api.POST("/assistant/threads/{thread}/messages", { params: { path: { thread: current } }, body: { text, context } }));
    },
    onSuccess: () => {
      setText("");
      void qc.invalidateQueries({ queryKey: ["assistant-thread", current] });
      void qc.invalidateQueries({ queryKey: ["revision-assistant"] });
    },
    onError: (e) => toast.error(errorMessage(e, t("errors.generic"))),
  });
  const accept = useMutation({
    mutationFn: (callID: string) => unwrap(api.POST("/assistant/threads/{thread}/proposals/{call}/accept", { params: { path: { thread: current!, call: callID } } })),
    onSuccess: (r) => {
      void qc.invalidateQueries({ queryKey: ["revision-assistant"] });
      void qc.invalidateQueries({ queryKey: ["revision-files"] });
      if ("revision" in r && r.revision) {
        toast.success(t("assistant.started", { title: r.revision.title }));
        void navigate({ to: "/$owner/$repo/revisions/$number", params: { owner: repo.owner, repo: repo.name, number: String(r.revision.number) } });
      }
    },
    onError: (e) => toast.error(errorMessage(e, t("errors.generic"))),
  });
  const msgs = thread.data?.messages ?? [];
  const running = live.running || !!thread.data?.running;
  useEffect(() => {
    endRef.current?.scrollIntoView({ block: "end" });
  }, [msgs.length, live.text, live.tools.length]);

  if (status.data && !status.data.enabled) {
    return (
      <div className="grid gap-2 text-[13px]">
        <h2 className="font-medium">{t("shell.panel.assistant")}</h2>
        <p className="text-muted-foreground">{me?.is_instance_admin ? t("assistant.offAdmin") : t("assistant.off")}</p>
      </div>
    );
  }
  return (
    <div className="-m-3.5 flex h-[calc(100%+1.75rem)] flex-col">
      {revision ? (
        <div className="border-b px-3.5 py-2.5 text-[12px] text-muted-foreground">{t("assistant.shared", { number: revision.number })}</div>
      ) : (
      <div className="flex items-center gap-1.5 border-b px-3 py-2">
        <Select value={current ?? "new"} onValueChange={(v) => setThreadID(v)}>
          <SelectTrigger size="sm" className="min-w-0 flex-1" aria-label={t("assistant.conversations")}>
            <SelectValue placeholder={t("assistant.new")} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="new">{t("assistant.new")}</SelectItem>
            {(threads.data ?? []).map((th) => (
              <SelectItem key={th.id} value={th.id}>
                {th.title || t("assistant.untitled")}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Button size="icon" variant="ghost" className="size-8" onClick={() => setThreadID("new")} aria-label={t("assistant.new")} title={t("assistant.new")}>
          <Plus />
        </Button>
      </div>
      )}
      <div className="min-h-0 flex-1 overflow-auto px-3.5 py-3">
        {!current && !revision && (
          <div className="grid justify-items-center gap-2 py-10 text-center text-[13px] text-muted-foreground">
            <Sparkles className="size-5" />
            <p>{revision ? t("assistant.introRevision") : t("assistant.intro")}</p>
          </div>
        )}
        <div className="grid gap-4">
          {msgs.map((m) => (
            <MessageItem key={m.id} m={m} repo={repo} revision={revision} canStart={revision ? canPrompt : atLeast(repo.role, "contributor")} onAccept={(id) => accept.mutate(id)} accepting={accept.isPending} />
          ))}
          {running && (
            <div className="grid gap-1.5">
              {live.tools.map((l, i) => (
                <div key={i} className="flex items-center gap-1.5 text-[12px] text-muted-foreground">
                  <Wrench className="size-3" />
                  {l}
                </div>
              ))}
              {live.text ? (
                <p className="text-[13.5px] whitespace-pre-wrap">{live.text}</p>
              ) : (
                <span className="flex items-center gap-1.5 text-[12px] text-muted-foreground">
                  <Loader2 className="size-3 animate-spin" />
                  {t("assistant.thinking")}
                </span>
              )}
              {live.queued && <span className="text-[12px] text-muted-foreground">{t("assistant.queued")}</span>}
            </div>
          )}
        </div>
        <div ref={endRef} />
      </div>
      {!canPrompt && <p className="border-t p-3 text-[12.5px] text-muted-foreground">{t("assistant.readOnly")}</p>}
      <form
        hidden={!canPrompt}
        className="border-t p-3"
        onSubmit={(e) => {
          e.preventDefault();
          if (text.trim()) send.mutate();
        }}
      >
        <div className="relative">
          <Textarea
            rows={2}
            value={text}
            onChange={(e) => setText(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
                e.preventDefault();
                if (text.trim()) send.mutate();
              }
            }}
            placeholder={t("assistant.placeholder")}
            aria-label={t("assistant.placeholder")}
            className="resize-none pr-11"
          />
          <Button type="submit" size="icon" className={cn("absolute right-2 bottom-2 size-7 rounded-full")} disabled={!text.trim() || send.isPending} aria-label={t("assistant.send")}>
            {send.isPending ? <Loader2 className="animate-spin" /> : <ArrowUp />}
          </Button>
        </div>
        {path && <p className="mt-1.5 truncate text-[11.5px] text-muted-foreground">{t("assistant.context", { path })}</p>}
      </form>
    </div>
  );
}
