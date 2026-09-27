import { useMemo } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";
import { toast } from "sonner";
import { Check, ChevronDown, X } from "lucide-react";
import { useEditorState, type Editor } from "@tiptap/react";
import type { Node as PMNode } from "@tiptap/pm/model";
import { TextSelection } from "@tiptap/pm/state";
import { Avatar } from "@/components/avatar";
import { Time } from "@/components/time";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { editorSuggestions, type EditorSuggestion } from "@/components/editor/suggest";
import { api, errorMessage, unwrap, useMe } from "@/lib/api";
import type { RevisionView } from "@/lib/revisions";
import type { RepoView } from "@/lib/repos";
import { useAnnounceChanges } from "@/lib/announce";

/** Pending suggestions in the editor's page, recomputed when the document changes. */
export function useEditorSuggestions(editor: Editor | null): EditorSuggestion[] {
  const doc = useEditorState({ editor, selector: ({ editor: e }) => (e ? e.state.doc : null), equalityFn: (a: PMNode | null, b: PMNode | null) => a === b });
  return useMemo(() => (doc ? editorSuggestions(doc) : []), [doc]);
}

type Resolve = { action: "accept" | "reject"; ids?: string[]; author?: string };

type Describable = { inserted: string; deleted: string; kinds: string[]; change?: { from: string; to: string } };

/** What a suggestion does, in words: "Add “…”", "Replace “…” with “…”", "Paragraph → Heading". */
export function describeSuggestion(t: TFunction, s: Describable): string {
  const q = (x: string) => `“${x.length > 80 ? x.slice(0, 79) + "…" : x.replace(/\n/g, " ")}”`;
  if (s.change && !s.inserted && !s.deleted) return t("suggestions.change", { from: t(`suggestions.nodes.${s.change.from}`, { defaultValue: s.change.from }), to: t(`suggestions.nodes.${s.change.to}`, { defaultValue: s.change.to }) });
  if (s.inserted && s.deleted) return t("suggestions.replace", { from: q(s.deleted), to: q(s.inserted) });
  if (s.inserted) return t("suggestions.add", { text: q(s.inserted) });
  if (s.deleted) return t("suggestions.delete", { text: q(s.deleted) });
  if (s.kinds.includes("join")) return t("suggestions.join");
  return t("suggestions.split");
}

function Card({ s, name, canResolve, busy, onResolve, onFocus }: { s: EditorSuggestion; name: string; canResolve: boolean; busy: boolean; onResolve: (r: Resolve) => void; onFocus: () => void }) {
  const { t } = useTranslation();
  const what = describeSuggestion(t, s);
  return (
    <div role="button" tabIndex={0} onClick={onFocus} onKeyDown={(e) => e.key === "Enter" && onFocus()} className="grid gap-1.5 rounded-lg border border-dashed bg-card p-3 text-left">
      <div className="flex items-center gap-2 text-[0.78125rem]">
        <Avatar name={name} id={s.author} size="xs" />
        <span className="font-medium">{s.assistant ? t("suggestions.viaAssistant", { name }) : name}</span>
        {s.at && <Time iso={new Date(s.at).toISOString()} className="text-muted-foreground" />}
        {canResolve && (
          <span className="ml-auto flex gap-0.5">
            <Button
              size="icon"
              variant="ghost"
              className="size-6"
              disabled={busy}
              aria-label={t("suggestions.accept")}
              title={t("suggestions.accept")}
              onClick={(e) => {
                e.stopPropagation();
                onResolve({ action: "accept", ids: [s.id] });
              }}
            >
              <Check className="text-success" />
            </Button>
            <Button
              size="icon"
              variant="ghost"
              className="size-6"
              disabled={busy}
              aria-label={t("suggestions.reject")}
              title={t("suggestions.reject")}
              onClick={(e) => {
                e.stopPropagation();
                onResolve({ action: "reject", ids: [s.id] });
              }}
            >
              <X className="text-destructive" />
            </Button>
          </span>
        )}
      </div>
      <p className="text-[0.8125rem] break-words">{what}</p>
    </div>
  );
}

/**
 * Suggestion cards for the Comments tab: one per pending suggestion, in
 * document order, with Accept/Reject and bulk actions per author.
 */
export function SuggestionList({ editor, rev, path }: { editor: Editor | null; rev: RevisionView; path: string }) {
  const { t } = useTranslation();
  const { data: me } = useMe();
  const items = useEditorSuggestions(editor);
  const names = useMemo(() => {
    const m = new Map<string, string>();
    for (const x of [...rev.members, ...rev.reviewers]) m.set(x.user_id, x.name);
    return m;
  }, [rev.members, rev.reviewers]);
  // Other people's (and the assistant's) new suggestions, read out.
  const theirs = useMemo(() => items.filter((s) => s.author !== me?.id || s.assistant), [items, me?.id]);
  useAnnounceChanges(me ? theirs : undefined, (s) => s.id, (s) =>
    t(s.assistant ? "suggestions.announceAssistant" : "suggestions.announce", { name: names.get(s.author) ?? t("revision.someone") }),
  );
  const resolve = useMutation({
    mutationFn: (r: Resolve) => unwrap(api.POST("/revisions/{revision}/suggestions/resolve", { params: { path: { revision: rev.id } }, body: { path, ...r } })),
    onError: (e) => toast.error(errorMessage(e, t("errors.generic"))),
  });
  if (!items.length) return null;
  const open = rev.state !== "published" && rev.state !== "closed";
  const canAll = open && rev.access.can_resolve;
  const can = (s: EditorSuggestion) => open && (rev.access.can_resolve || s.author === me?.id);
  const authors = [...new Set(items.map((s) => s.author))];
  const name = (id: string) => names.get(id) ?? (id === me?.id ? (me?.name ?? "") : t("revision.someone"));
  const focus = (s: EditorSuggestion) => {
    if (!editor || editor.isDestroyed) return;
    const to = Math.min(s.to, editor.state.doc.content.size);
    editor.view.dispatch(editor.state.tr.setSelection(TextSelection.create(editor.state.doc, Math.min(s.from, to), to)).scrollIntoView());
    editor.view.focus();
  };
  return (
    <section aria-label={t("suggestions.title")} className="grid gap-2">
      <div className="flex items-center gap-1 px-1 text-[0.75rem] text-muted-foreground">
        <span className="font-medium text-foreground">{t("suggestions.count", { count: items.length })}</span>
        {canAll && (
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant="ghost" size="sm" className="ml-auto h-6 px-2 text-[0.75rem]" disabled={resolve.isPending}>
                {t("suggestions.bulk")}
                <ChevronDown />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuItem onSelect={() => resolve.mutate({ action: "accept" })}>{t("suggestions.acceptAll")}</DropdownMenuItem>
              <DropdownMenuItem onSelect={() => resolve.mutate({ action: "reject" })}>{t("suggestions.rejectAll")}</DropdownMenuItem>
              {authors.length > 1 &&
                authors.map((a) => (
                  <DropdownMenuItem key={a} onSelect={() => resolve.mutate({ action: "accept", author: a })}>
                    {t("suggestions.acceptFrom", { name: name(a) })}
                  </DropdownMenuItem>
                ))}
            </DropdownMenuContent>
          </DropdownMenu>
        )}
      </div>
      {items.map((s) => (
        <Card key={s.id} s={s} name={name(s.author)} canResolve={can(s)} busy={resolve.isPending} onResolve={(r) => resolve.mutate(r)} onFocus={() => focus(s)} />
      ))}
    </section>
  );
}

/**
 * Every pending suggestion in the revision, for its overview: they block
 * approval and publishing until someone accepts or rejects them.
 */
export function RevisionSuggestions({ repo, rev }: { repo: RepoView; rev: RevisionView }) {
  const { t } = useTranslation();
  const { data: me } = useMe();
  const qc = useQueryClient();
  const list = useQuery({
    queryKey: ["revision-suggestions", rev.id, rev.updated_at, rev.pending_suggestions],
    queryFn: async () => (await unwrap(api.GET("/revisions/{revision}/suggestions", { params: { path: { revision: rev.id } } }))).items,
    enabled: rev.pending_suggestions > 0,
  });
  const names = useMemo(() => {
    const m = new Map<string, string>();
    for (const x of [...rev.members, ...rev.reviewers]) m.set(x.user_id, x.name);
    return m;
  }, [rev.members, rev.reviewers]);
  const resolve = useMutation({
    mutationFn: (r: Resolve & { path: string }) => unwrap(api.POST("/revisions/{revision}/suggestions/resolve", { params: { path: { revision: rev.id } }, body: r })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["revision-suggestions", rev.id] });
      void qc.invalidateQueries({ queryKey: ["revision", repo.id, rev.number] });
    },
    onError: (e) => toast.error(errorMessage(e, t("errors.generic"))),
  });
  if (rev.pending_suggestions === 0 || !list.data?.length) return null;
  const open = rev.state !== "published" && rev.state !== "closed";
  const name = (id: string, fallback?: string) => names.get(id) ?? fallback ?? (id === me?.id ? (me?.name ?? "") : t("revision.someone"));
  return (
    <section id="suggestions" aria-label={t("suggestions.toAddress")}>
      <h2 className="mb-2 flex items-center gap-2 text-[0.9375rem] font-semibold">
        {t("suggestions.toAddress")}
        <span className="rounded-md bg-warning/10 px-1.5 text-[0.75rem] font-medium text-warning">{list.data.length}</span>
      </h2>
      <p className="mb-2 text-[0.8125rem] text-muted-foreground">{t("suggestions.toAddressHint")}</p>
      <ul className="overflow-hidden rounded-xl border">
        {list.data.map((s) => {
          const can = open && (rev.access.can_resolve || s.author === me?.id);
          return (
            <li key={s.path + s.id} className="flex items-start gap-3 border-b px-4 py-2.5 text-[0.84375rem] last:border-b-0">
              <Avatar name={name(s.author, s.author_name)} id={s.author} size="xs" />
              <div className="min-w-0 flex-1">
                <p className="break-words">{describeSuggestion(t, s)}</p>
                <p className="text-[0.75rem] text-muted-foreground">
                  {name(s.author, s.author_name)} ·{" "}
                  <Link to="/$org/$owner/$repo/$" params={{ org: repo.org_slug, owner: repo.owner, repo: repo.name, _splat: s.path }} search={{ revision: rev.number }} className="font-mono hover:text-foreground hover:underline">
                    {s.path}
                  </Link>
                  {s.at ? (
                    <>
                      {" · "}
                      <Time iso={new Date(s.at).toISOString()} />
                    </>
                  ) : null}
                </p>
              </div>
              {can && (
                <span className="flex shrink-0 gap-1">
                  <Button size="sm" variant="outline" className="h-7" disabled={resolve.isPending} onClick={() => resolve.mutate({ path: s.path, action: "accept", ids: [s.id] })}>
                    <Check className="text-success" />
                    <span className="max-md:sr-only">{t("suggestions.accept")}</span>
                  </Button>
                  <Button size="sm" variant="ghost" className="h-7" disabled={resolve.isPending} onClick={() => resolve.mutate({ path: s.path, action: "reject", ids: [s.id] })}>
                    <X className="text-destructive" />
                    <span className="max-md:sr-only">{t("suggestions.reject")}</span>
                  </Button>
                </span>
              )}
            </li>
          );
        })}
      </ul>
    </section>
  );
}
