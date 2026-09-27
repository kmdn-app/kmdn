import { Extension, type Editor } from "@tiptap/core";
import { Plugin, PluginKey, type EditorState } from "@tiptap/pm/state";
import { Decoration, DecorationSet } from "@tiptap/pm/view";
import * as Y from "yjs";
import { absolutePositionToRelativePosition, relativePositionToAbsolutePosition, ySyncPluginKey } from "@tiptap/y-tiptap";

export type AnchorView = {
  /** Threads to hide (resolved). */
  hidden: Set<string>;
  /** The thread shown as active. */
  active: string | null;
  /** Called when someone clicks an anchored passage. */
  onClick?: (threadID: string) => void;
};

type AnchorState = { view: AnchorView; decos: DecorationSet };

export const commentAnchorsKey = new PluginKey<AnchorState>("commentAnchors");

export type AnchorOptions = {
  /** The page's Y.Doc (the anchors live in its "comments" map). */
  doc: Y.Doc | null;
};

type Binding = { type: Y.XmlFragment; doc: Y.Doc; binding: { mapping: Map<unknown, unknown> } };

function binding(state: EditorState): Binding | null {
  const s = ySyncPluginKey.getState(state) as Binding | undefined;
  return s?.binding ? s : null;
}

type Pos = { start: unknown; end: unknown };

function decorate(state: EditorState, doc: Y.Doc | null, view: AnchorView): DecorationSet {
  const b = binding(state);
  if (!b || !doc) return DecorationSet.empty;
  const decos: Decoration[] = [];
  doc.getMap<Pos>("comments").forEach((v, id) => {
    if (view.hidden.has(id) || !v?.start || !v?.end) return;
    try {
      const from = relativePositionToAbsolutePosition(b.doc, b.type, Y.createRelativePositionFromJSON(v.start), b.binding.mapping as never);
      const to = relativePositionToAbsolutePosition(b.doc, b.type, Y.createRelativePositionFromJSON(v.end), b.binding.mapping as never);
      if (from !== null && to !== null && to > from) {
        decos.push(Decoration.inline(from, to, { class: id === view.active ? "comment-anchor is-active" : "comment-anchor", "data-thread": id }));
      }
    } catch {
      // A position from a deleted range: the thread shows as detached.
    }
  });
  return DecorationSet.create(state.doc, decos);
}

/**
 * Highlights passages that have comment threads (docs/specs/07-review.md#comments).
 * Anchors are Yjs relative positions, so they move with concurrent edits.
 */
export const CommentAnchors = Extension.create<AnchorOptions>({
  name: "commentAnchors",
  addOptions: () => ({ doc: null }),
  addProseMirrorPlugins() {
    const doc = this.options.doc;
    return [
      new Plugin<AnchorState>({
        key: commentAnchorsKey,
        state: {
          init: () => ({ view: { hidden: new Set(), active: null }, decos: DecorationSet.empty }),
          apply: (tr, old, _o, state) => {
            const meta = tr.getMeta(commentAnchorsKey) as AnchorView | true | undefined;
            const view = meta && meta !== true ? meta : old.view;
            if (meta || tr.docChanged || tr.getMeta(ySyncPluginKey)) return { view, decos: decorate(state, doc, view) };
            return { view, decos: old.decos.map(tr.mapping, tr.doc) };
          },
        },
        props: {
          decorations: (state) => commentAnchorsKey.getState(state)?.decos,
          handleClick: (view, _pos, event) => {
            const id = (event.target as HTMLElement | null)?.closest?.("[data-thread]")?.getAttribute("data-thread");
            if (id) commentAnchorsKey.getState(view.state)?.view.onClick?.(id);
            return false;
          },
        },
        view: (view) => {
          const map = doc?.getMap("comments");
          const refresh = () => view.dispatch(view.state.tr.setMeta(commentAnchorsKey, true).setMeta("addToHistory", false));
          map?.observe(refresh);
          // The first decoration pass needs the Yjs binding, which exists after the first render.
          const t = setTimeout(refresh, 0);
          return { destroy: () => (clearTimeout(t), map?.unobserve(refresh)) };
        },
      }),
    ];
  },
});

/** Sets which anchors show, which is active, and what a click does. */
export function setAnchorView(editor: Editor | null, v: AnchorView) {
  if (editor && !editor.isDestroyed) editor.view.dispatch(editor.state.tr.setMeta(commentAnchorsKey, v).setMeta("addToHistory", false));
}

/** The current selection as a Yjs relative range and its quote, for a new thread. */
export function selectionAnchor(editor: Editor): { position: { start: unknown; end: unknown }; quote: string } | null {
  const { from, to, empty } = editor.state.selection;
  const b = binding(editor.state);
  if (empty || !b) return null;
  const start = absolutePositionToRelativePosition(from, b.type, b.binding.mapping as never);
  const end = absolutePositionToRelativePosition(to, b.type, b.binding.mapping as never);
  return {
    position: { start: Y.relativePositionToJSON(start), end: Y.relativePositionToJSON(end) },
    quote: editor.state.doc.textBetween(from, to, " ").slice(0, 500),
  };
}
