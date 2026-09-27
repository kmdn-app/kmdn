import { useMemo } from "react";
import { useMutation } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
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

/** Pending suggestions in the editor's page, recomputed when the document changes. */
export function useEditorSuggestions(editor: Editor | null): EditorSuggestion[] {
  const doc = useEditorState({ editor, selector: ({ editor: e }) => (e ? e.state.doc : null), equalityFn: (a: PMNode | null, b: PMNode | null) => a === b });
  return useMemo(() => (doc ? editorSuggestions(doc) : []), [doc]);
}

type Resolve = { action: "accept" | "reject"; ids?: string[]; author?: string };

function Card({ s, name, canResolve, busy, onResolve, onFocus }: { s: EditorSuggestion; name: string; canResolve: boolean; busy: boolean; onResolve: (r: Resolve) => void; onFocus: () => void }) {
  const { t } = useTranslation();
  const q = (x: string) => `“${x.length > 80 ? x.slice(0, 79) + "…" : x.replace(/\n/g, " ")}”`;
  let what: string;
  if (s.change && !s.inserted && !s.deleted) what = t("suggestions.change", { from: t(`suggestions.nodes.${s.change.from}`, { defaultValue: s.change.from }), to: t(`suggestions.nodes.${s.change.to}`, { defaultValue: s.change.to }) });
  else if (s.inserted && s.deleted) what = t("suggestions.replace", { from: q(s.deleted), to: q(s.inserted) });
  else if (s.inserted) what = t("suggestions.add", { text: q(s.inserted) });
  else if (s.deleted) what = t("suggestions.delete", { text: q(s.deleted) });
  else if (s.kinds.includes("join")) what = t("suggestions.join");
  else what = t("suggestions.split");
  return (
    <div role="button" tabIndex={0} onClick={onFocus} onKeyDown={(e) => e.key === "Enter" && onFocus()} className="grid gap-1.5 rounded-lg border border-dashed bg-card p-3 text-left">
      <div className="flex items-center gap-2 text-[12.5px]">
        <Avatar name={name} id={s.author} size="xs" />
        <span className="font-medium">{name}</span>
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
      <p className="text-[13px] break-words">{what}</p>
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
      <div className="flex items-center gap-1 px-1 text-[12px] text-muted-foreground">
        <span className="font-medium text-foreground">{t("suggestions.count", { count: items.length })}</span>
        {canAll && (
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant="ghost" size="sm" className="ml-auto h-6 px-2 text-[12px]" disabled={resolve.isPending}>
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
