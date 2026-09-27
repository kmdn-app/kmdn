import { Extension } from "@tiptap/core";
import { Plugin, PluginKey } from "@tiptap/pm/state";
import { Decoration, DecorationSet } from "@tiptap/pm/view";
import type { Node as PMNode } from "@tiptap/pm/model";
import { nodeHash, type DocNode } from "@kmdn/doc-engine";

export const changeMarksKey = new PluginKey("changeMarks");

/** Longest common subsequence of hash lists, as index pairs. */
function lcs(a: string[], b: string[]): [number, number][] {
  const dp: Uint32Array[] = Array.from({ length: a.length + 1 }, () => new Uint32Array(b.length + 1));
  for (let i = a.length - 1; i >= 0; i--) for (let j = b.length - 1; j >= 0; j--) dp[i]![j] = a[i] === b[j] ? dp[i + 1]![j + 1]! + 1 : Math.max(dp[i + 1]![j]!, dp[i]![j + 1]!);
  const out: [number, number][] = [];
  let i = 0;
  let j = 0;
  while (i < a.length && j < b.length) {
    if (a[i] === b[j]) {
      out.push([i, j]);
      i++;
      j++;
    } else if (dp[i + 1]![j]! >= dp[i]![j + 1]!) i++;
    else j++;
  }
  return out;
}

type Block = { type: string; text: string; hash: string };

function blocksOfJSON(doc: DocNode): Block[] {
  const text = (n: unknown): string => {
    const x = n as { text?: string; content?: unknown[] };
    return (x.text ?? "") + (x.content ?? []).map(text).join("");
  };
  return doc.content.map((b) => ({ type: b.type, text: text(b), hash: nodeHash(b) }));
}

/**
 * Result view marks (docs/specs/07-review.md#result-and-changes-views): a
 * gutter bar on blocks that differ from the published base and a light
 * highlight on inserted text. Deleted content isn't shown here (that's the
 * Changes view).
 */
function decorate(doc: PMNode, base: Block[]): DecorationSet {
  const cur: { node: PMNode; pos: number; b: Block }[] = [];
  doc.forEach((node, pos) => {
    cur.push({ node, pos, b: { type: node.type.name, text: node.textContent, hash: nodeHash(node.toJSON()) } });
  });
  const pairs = lcs(
    base.map((b) => b.hash),
    cur.map((c) => c.b.hash),
  );
  const decos: Decoration[] = [];
  const anchors: [number, number][] = [[-1, -1], ...pairs, [base.length, cur.length]];
  for (let g = 1; g < anchors.length; g++) {
    const [a0, b0] = anchors[g - 1]!;
    const [a1, b1] = anchors[g]!;
    const olds = base.slice(a0 + 1, a1);
    for (let k = b0 + 1; k < b1; k++) {
      const c = cur[k]!;
      decos.push(Decoration.node(c.pos, c.pos + c.node.nodeSize, { class: "chg-block" }));
      // A changed text block paired with the old one: highlight what's new.
      const old = olds[k - b0 - 1];
      if (old && old.type === c.b.type && c.node.isTextblock) {
        const a = old.text;
        const b = c.b.text;
        let pre = 0;
        while (pre < a.length && pre < b.length && a[pre] === b[pre]) pre++;
        let suf = 0;
        while (suf < a.length - pre && suf < b.length - pre && a[a.length - 1 - suf] === b[b.length - 1 - suf]) suf++;
        const from = c.pos + 1 + pre;
        const to = c.pos + 1 + b.length - suf;
        if (to > from) decos.push(Decoration.inline(from, to, { class: "chg-ins" }));
      } else if (c.node.isTextblock && c.node.content.size > 0) {
        decos.push(Decoration.inline(c.pos + 1, c.pos + c.node.nodeSize - 1, { class: "chg-ins" }));
      }
    }
  }
  return DecorationSet.create(doc, decos);
}

type MarksState = { base: Block[] | null; decos: DecorationSet };

/**
 * Marks changes against a base (the page at the revision's base). The base
 * arrives after the editor is created: set it with setChangeBase.
 */
export const ChangeMarks = Extension.create({
  name: "changeMarks",
  addProseMirrorPlugins() {
    return [
      new Plugin<MarksState>({
        key: changeMarksKey as unknown as PluginKey<MarksState>,
        state: {
          init: () => ({ base: null, decos: DecorationSet.empty }),
          apply: (tr, old, _o, state) => {
            const meta = tr.getMeta(changeMarksKey) as { base: DocNode | null } | undefined;
            if (meta) {
              const base = meta.base ? blocksOfJSON(meta.base) : null;
              return { base, decos: base ? decorate(state.doc, base) : DecorationSet.empty };
            }
            if (!old.base || !tr.docChanged) return { base: old.base, decos: old.decos.map(tr.mapping, tr.doc) };
            return { base: old.base, decos: decorate(state.doc, old.base) };
          },
        },
        props: {
          decorations: (state) => (changeMarksKey as unknown as PluginKey<MarksState>).getState(state)?.decos,
        },
      }),
    ];
  },
});

/** Shows (or, with null, hides) change marks against base. */
export function setChangeBase(view: { state: { tr: import("@tiptap/pm/state").Transaction }; dispatch: (tr: import("@tiptap/pm/state").Transaction) => void }, base: DocNode | null) {
  view.dispatch(view.state.tr.setMeta(changeMarksKey, { base }).setMeta("addToHistory", false));
}
