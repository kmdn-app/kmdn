import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { Annotation, ChangeSet, Compartment, EditorState } from "@codemirror/state";
import { EditorView, drawSelection, highlightActiveLine, keymap, lineNumbers } from "@codemirror/view";
import { defaultKeymap, history, historyKeymap, indentWithTab } from "@codemirror/commands";
import { markdown } from "@codemirror/lang-markdown";
import { HighlightStyle, syntaxHighlighting } from "@codemirror/language";
import { tags } from "@lezer/highlight";
import { SourceSync, textChange, type SourceMap } from "@kmdn/doc-engine";
import type { RoomProvider } from "@/lib/realtime";
import { getSourceBufferError, markSourceUpdate, registerSourceBuffer, setSourceBufferError } from "@/lib/source-buffers";
import { sourceChanges } from "@/lib/source-changes";

const remote = Annotation.define<boolean>();

const editorText = (source: string) => source.replace(/\r\n?/g, "\n");

/** CodeMirror counts every line break once; unchanged source keeps its bytes. */
function applySourceChanges(source: string, changes: ChangeSet): string {
  const separator = /\r\n|\r|\n/.exec(source)?.[0] ?? "\n";
  let raw = 0;
  let normalized = 0;
  const offset = (position: number) => {
    while (normalized < position && raw < source.length) {
      if (source[raw] === "\r" && source[raw + 1] === "\n") raw++;
      raw++;
      normalized++;
    }
    return raw;
  };
  let result = "";
  let copied = 0;
  changes.iterChanges((from, to, _fromB, _toB, insert) => {
    const start = offset(from);
    const end = offset(to);
    result += source.slice(copied, start) + insert.toString().replace(/\n/g, separator);
    copied = end;
  });
  return result + source.slice(copied);
}

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
  const [error, setError] = useState<string | null>(null);

  // Flush during layout cleanup, before the parent's passive room cleanup
  // unsubscribes and destroys the document.
  useLayoutEffect(() => {
    let cancelled = false;
    let cleanup: (() => void) | undefined;
    void loadSourceMap(revisionID, path).then((sm) => {
      if (cancelled || !host.current) return;
      const sync = new SourceSync(provider.doc, sm);
      const recovery = getSourceBufferError(revisionID, path);
      let source = recovery?.base ?? sync.text();
      let base = editorText(source);
      let draft = recovery?.source ?? source;
      let pending = recovery?.changes ?? sourceChanges(base, editorText(draft));
      let timer: ReturnType<typeof setTimeout> | undefined;
      let rebaseError: Error | undefined;
      let failedHere = recovery?.source !== undefined;
      if (recovery) setError(recovery.message);
      const failed = (reason: unknown) => {
        const message = reason instanceof RangeError ? "This source is too large to sync. Shorten it before saving, or copy it before leaving this page." : reason instanceof Error ? reason.message : "Source edits could not be synced. Copy them before leaving this page.";
        setSourceBufferError(revisionID, path, message, draft, source, pending);
        failedHere = true;
        if (!cancelled) setError(message);
        return new Error(message);
      };
      const receive = () => {
        try {
          const nextSource = sync.text();
          const next = editorText(nextSource);
          // With no local edits there are no positions to preserve.
          const theirs = pending.empty
            ? ChangeSet.of(textChange(base, next) ?? [], base.length)
            : sourceChanges(base, next);
          const visible = theirs.map(pending, true);
          const mapped = pending.map(theirs);
          const nextDraft = applySourceChanges(nextSource, mapped);
          source = nextSource;
          base = next;
          pending = mapped;
          draft = nextDraft;
          rebaseError = undefined;
          return visible;
        } catch (reason) {
          rebaseError = failed(reason);
          return undefined;
        }
      };
      receive();
      const flush = () => {
        if (timer) clearTimeout(timer);
        timer = undefined;
        try {
          if (rebaseError) throw rebaseError;
          if (!pending.empty || failedHere) {
            const text = draft;
            sync.apply(text);
            markSourceUpdate(revisionID);
            source = text;
            base = v.state.doc.toString();
            pending = ChangeSet.empty(base.length);
            setSourceBufferError(revisionID, path);
            failedHere = false;
            if (!cancelled) setError(null);
          }
        } catch (reason) {
          throw failed(reason);
        }
      };
      const tryFlush = () => { try { flush(); } catch { /* The buffer and its Save error remain available. */ } };
      const v = new EditorView({
        parent: host.current,
        state: EditorState.create({
          doc: editorText(draft),
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
            EditorView.domEventHandlers({ blur: () => tryFlush() }),
            EditorView.updateListener.of((u) => {
              if (!u.docChanged || u.transactions.some((t) => t.annotation(remote))) return;
              for (const t of u.transactions) {
                draft = applySourceChanges(draft, t.changes);
                pending = pending.compose(t.changes);
              }
              if (timer) clearTimeout(timer);
              timer = setTimeout(tryFlush, 250);
            }),
          ],
        }),
      });
      view.current = v;
      const unregister = registerSourceBuffer(revisionID, flush);
      const onUpdate = (_u: Uint8Array, origin: unknown) => {
        if (origin === sync.origin) return;
        const changes = receive();
        if (!changes) return;
        if (!changes.empty) v.dispatch({ changes, annotations: remote.of(true) });
        if (!pending.empty) tryFlush();
      };
      provider.doc.on("update", onUpdate);
      provider.awareness.setLocalStateField("mode", "source");
      cleanup = () => {
        unregister();
        provider.doc.off("update", onUpdate);
        tryFlush();
        v.destroy();
        view.current = null;
        provider.awareness.setLocalStateField("mode", "wysiwyg");
      };
    }).catch((reason: unknown) => {
      if (!cancelled) setError(reason instanceof Error ? reason.message : "Source could not be loaded.");
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

  return <div className="mx-auto max-w-[53.75rem] px-6 max-md:px-2">{error && <p role="alert" className="pt-4 text-destructive">{error}</p>}<div ref={host} /></div>;
}
