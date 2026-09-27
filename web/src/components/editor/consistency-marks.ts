import { Extension, type Editor } from "@tiptap/core";
import type { Node as PMNode } from "@tiptap/pm/model";
import { Plugin, PluginKey } from "@tiptap/pm/state";
import { Decoration, DecorationSet } from "@tiptap/pm/view";

/** A passage to underline: the quoted claim, found in the page's text. */
export type ConsistencyMark = { id: string; kind: "contradiction" | "duplicate"; quote: string };

type State = { marks: ConsistencyMark[]; decos: DecorationSet };

const key = new PluginKey<State>("consistencyMarks");

/** Where quote occurs in a textblock, as document positions (case-insensitive). */
export function findQuote(doc: PMNode, quote: string): { from: number; to: number } | null {
  const q = quote.trim().toLowerCase();
  if (!q) return null;
  let hit: { from: number; to: number } | null = null;
  doc.descendants((node, pos) => {
    if (hit) return false;
    if (!node.isTextblock) return true;
    // Text of the block, with the document position of each character.
    let text = "";
    const at: number[] = [];
    node.forEach((child, offset) => {
      if (!child.isText) return;
      for (let i = 0; i < child.text!.length; i++) at.push(pos + 1 + offset + i);
      text += child.text;
    });
    const i = text.toLowerCase().indexOf(q);
    if (i >= 0) hit = { from: at[i]!, to: at[i + q.length - 1]! + 1 };
    return false;
  });
  return hit;
}

function decorate(doc: PMNode, marks: ConsistencyMark[]): DecorationSet {
  const decos: Decoration[] = [];
  for (const m of marks) {
    const r = findQuote(doc, m.quote);
    if (r) decos.push(Decoration.inline(r.from, r.to, { class: `consistency-mark is-${m.kind}`, "data-finding": m.id }));
  }
  return DecorationSet.create(doc, decos);
}

/**
 * Underlines passages with consistency findings (docs/specs/08-assistant.md#per-revision);
 * the hover card is rendered by the page around the editor.
 */
export const ConsistencyMarks = Extension.create({
  name: "consistencyMarks",
  addProseMirrorPlugins() {
    return [
      new Plugin<State>({
        key,
        state: {
          init: () => ({ marks: [], decos: DecorationSet.empty }),
          apply: (tr, old, _o, state) => {
            const meta = tr.getMeta(key) as ConsistencyMark[] | undefined;
            const marks = meta ?? old.marks;
            if (meta || tr.docChanged) return { marks, decos: marks.length ? decorate(state.doc, marks) : DecorationSet.empty };
            return old;
          },
        },
        props: { decorations: (state) => key.getState(state)?.decos },
      }),
    ];
  },
});

export function setConsistencyMarks(editor: Editor | null, marks: ConsistencyMark[]) {
  if (editor && !editor.isDestroyed) editor.view.dispatch(editor.state.tr.setMeta(key, marks).setMeta("addToHistory", false));
}
