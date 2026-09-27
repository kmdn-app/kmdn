import { useEffect, useLayoutEffect, useRef } from "react";
import { Annotation, ChangeSet, Compartment, EditorState } from "@codemirror/state";
import { EditorView, drawSelection, highlightActiveLine, keymap, lineNumbers } from "@codemirror/view";
import { defaultKeymap, history, historyKeymap, indentWithTab } from "@codemirror/commands";
import { markdown } from "@codemirror/lang-markdown";
import { HighlightStyle, syntaxHighlighting } from "@codemirror/language";
import { tags } from "@lezer/highlight";
import { SourceSync, textChange, type SourceMap } from "@kmdn/doc-engine";
import type { RoomProvider } from "@/lib/realtime";
import { markSourceUpdate, registerSourceBuffer } from "@/lib/source-buffers";

const remote = Annotation.define<boolean>();

const highlight = HighlightStyle.define([
  { tag: tags.heading, fontWeight: "600", color: "var(--foreground)" },
  { tag: tags.strong, fontWeight: "600" },
  { tag: tags.emphasis, fontStyle: "italic" },
  { tag: tags.strikethrough, textDecoration: "line-through" },
  { tag: [tags.link, tags.url], color: "var(--primary)", textDecoration: "underline" },
  { tag: [tags.monospace, tags.processingInstruction], color: "color-mix(in oklab, var(--foreground) 80%, var(--primary))" },
  { tag: [tags.meta, tags.comment, tags.contentSeparator, tags.quote], color: "var(--muted-foreground)" },
]);

const theme = EditorView.theme({
  "&": { fontSize: "0.875rem", backgroundColor: "transparent", color: "var(--foreground)" },
  ".cm-content": { fontFamily: "var(--font-mono)", padding: "1.5rem 0 10rem", caretColor: "var(--foreground)" },
  ".cm-gutters": { backgroundColor: "transparent", border: "none", color: "var(--muted-foreground)" },
  ".cm-activeLine": { backgroundColor: "color-mix(in oklab, var(--muted) 60%, transparent)" },
  "&.cm-focused": { outline: "none" },
  ".cm-selectionBackground, &.cm-focused .cm-selectionBackground": { backgroundColor: "color-mix(in oklab, var(--primary) 25%, transparent)" },
});

async function loadSourceMap(revisionID: string, path: string): Promise<SourceMap | undefined> {
  const res = await fetch(`/api/v1/revisions/${encodeURIComponent(revisionID)}/sourcemap/${path.split("/").map(encodeURIComponent).join("/")}`, { credentials: "same-origin" });
  return res.status === 200 ? ((await res.json()) as SourceMap) : undefined;
}

/**
 * Live source mode (docs/specs/05-collaboration.md): CodeMirror on the page's
 * markdown, kept in sync with the room's Y.Doc through SourceSync.
 *
 * Local typing is applied after a short pause. A collaborator's change that
 * arrives in between is rebased: CodeMirror knows the text last in sync with
 * the document (base) and the local changes since (pending); the remote change
 * is mapped over the pending ones, so neither side's edits are lost and the
 * cursor stays where it was.
 */
export function SourceEditor({ provider, revisionID, path, editable }: { provider: RoomProvider; revisionID: string; path: string; editable: boolean }) {
  const host = useRef<HTMLDivElement>(null);
  const view = useRef<EditorView | null>(null);
  const readOnly = useRef(new Compartment());
  const editableRef = useRef(editable);

  // Flush during layout cleanup, before the parent's passive room cleanup
  // unsubscribes and destroys the document.
  useLayoutEffect(() => {
    let cancelled = false;
    let cleanup: (() => void) | undefined;
    void loadSourceMap(revisionID, path).then((sm) => {
      if (cancelled || !host.current) return;
      const sync = new SourceSync(provider.doc, sm);
      let base = sync.text();
      let pending = ChangeSet.empty(base.length);
      let timer: ReturnType<typeof setTimeout> | undefined;
      const flush = () => {
        if (timer) clearTimeout(timer);
        timer = undefined;
        if (pending.empty) return;
        const text = v.state.doc.toString();
        sync.apply(text);
        markSourceUpdate(revisionID);
        base = text;
        pending = ChangeSet.empty(text.length);
      };
      const v = new EditorView({
        parent: host.current,
        state: EditorState.create({
          doc: base,
          extensions: [
            lineNumbers(),
            history(),
            drawSelection(),
            highlightActiveLine(),
            keymap.of([...defaultKeymap, ...historyKeymap, indentWithTab]),
            markdown(),
            syntaxHighlighting(highlight),
            EditorView.lineWrapping,
            theme,
            readOnly.current.of(EditorState.readOnly.of(!editableRef.current)),
            EditorView.contentAttributes.of({ "aria-label": "Markdown source", spellcheck: "true" }),
            EditorView.domEventHandlers({ blur: () => flush() }),
            EditorView.updateListener.of((u) => {
              if (!u.docChanged || u.transactions.some((t) => t.annotation(remote))) return;
              for (const t of u.transactions) pending = pending.compose(t.changes);
              if (timer) clearTimeout(timer);
              timer = setTimeout(flush, 250);
            }),
          ],
        }),
      });
      view.current = v;
      const unregister = registerSourceBuffer(revisionID, flush);
      const onUpdate = (_u: Uint8Array, origin: unknown) => {
        if (origin === sync.origin) return;
        const next = sync.text();
        const change = textChange(base, next);
        if (!change) return;
        const theirs = ChangeSet.of([change], base.length);
        // Their change, placed after ours; ours, moved past theirs.
        v.dispatch({ changes: theirs.map(pending, true), annotations: remote.of(true) });
        pending = pending.map(theirs);
        base = next;
        if (!pending.empty) flush();
      };
      provider.doc.on("update", onUpdate);
      provider.awareness.setLocalStateField("mode", "source");
      cleanup = () => {
        unregister();
        provider.doc.off("update", onUpdate);
        flush();
        v.destroy();
        view.current = null;
        provider.awareness.setLocalStateField("mode", "wysiwyg");
      };
    });
    return () => {
      cancelled = true;
      cleanup?.();
    };
  }, [provider, revisionID, path]);

  useEffect(() => {
    editableRef.current = editable;
    view.current?.dispatch({ effects: readOnly.current.reconfigure(EditorState.readOnly.of(!editable)) });
  }, [editable]);

  return <div ref={host} className="mx-auto max-w-[53.75rem] px-6 max-md:px-2" />;
}
