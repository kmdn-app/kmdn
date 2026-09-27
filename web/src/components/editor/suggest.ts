/**
 * Suggesting mode (docs/specs/05-collaboration.md#suggestions-tracked-changes):
 * a user's transactions are rewritten before they reach the editor, so the
 * change is recorded instead of applied.
 *
 * - Inserted content gets an `insertion` mark; nodes that start inside it get
 *   a `suggestion: {kind: "insert"}` attribute (a split paragraph, a pasted
 *   block).
 * - Deleted content stays, with a `deletion` mark. A deletion across a block
 *   boundary marks the next block `join`; blocks deleted whole are marked
 *   `delete`. Deleting your own pending insertion removes it for real.
 * - Type and attribute changes (paragraph → heading, a task checked, a math
 *   block edited) mark the node `change` with its previous type and attrs.
 *
 * Everything else (wrapping in a list, table structure, formatting marks)
 * can't be suggested yet: those transactions are refused. What pending
 * suggestions mean for the page lives in @kmdn/doc-engine (suggestions.ts).
 *
 * Positions: each step of the original transaction was computed against a
 * document without the content we kept, so it's mapped into ours through
 * `map` (original → ours). The inverted original step and our insertion are
 * registered as mirrors, so positions inside inserted content survive.
 */
import { Extension } from "@tiptap/core";
import type { Mark, Node as PMNode } from "@tiptap/pm/model";
import { Selection, type EditorState, type Transaction } from "@tiptap/pm/state";
import { AttrStep, Mapping, ReplaceAroundStep, ReplaceStep, replaceStep } from "@tiptap/pm/transform";
import { ySyncPluginKey } from "@tiptap/y-tiptap";
import { DIRECT_EDIT } from "./schema";
import { canonical, type NodeSuggestion, type SuggestionInfo, type SuggestionKind } from "@kmdn/doc-engine";

export type SuggestContext = { author: string; now: number; newId: () => string };

export function newSuggestionId(): string {
  return "sg_" + Date.now().toString(36) + Math.random().toString(36).slice(2, 8);
}

const sugOf = (n: PMNode) => (n.attrs.suggestion ?? null) as NodeSuggestion | null;
const markOf = (n: PMNode | null | undefined, type: string): Mark | undefined => n?.marks.find((m) => m.type.name === type);
const ownInsertion = (n: PMNode, author: string) => markOf(n, "insertion")?.attrs.author === author;
const ownInsertedNode = (n: PMNode, author: string) => {
  const s = sugOf(n);
  return s?.kind === "insert" && s.author === author;
};

/** Attributes as the doc engine compares them (no suggestion, no source id). */
function plainAttrs(attrs: Record<string, unknown>): Record<string, unknown> {
  return Object.fromEntries(Object.entries(attrs).filter(([k]) => k !== "suggestion" && k !== "sid"));
}

/** Whether [from, to) holds only content this author suggested adding. */
function onlyOwn(doc: PMNode, from: number, to: number, author: string): boolean {
  let own = true;
  doc.nodesBetween(from, to, (node, pos) => {
    if (!own) return false;
    if (node.isInline) {
      if (!ownInsertion(node, author)) own = false;
      return false;
    }
    if (pos >= from && !ownInsertedNode(node, author)) own = false;
    return true;
  });
  return own;
}

/** The id of this author's suggestion of this type right before `from` or after `to`, to extend it. */
function adjacentId(doc: PMNode, from: number, to: number, type: string, author: string): string | null {
  const $from = doc.resolve(from);
  for (const n of [$from.nodeBefore, doc.resolve(to).nodeAfter]) {
    const m = markOf(n, type);
    if (m && m.attrs.author === author) return m.attrs.id as string;
  }
  // Typing at the start of a paragraph you just split off: the same suggestion.
  if (type === "insertion" && $from.parentOffset === 0 && ownInsertedNode($from.parent, author)) return sugOf($from.parent)!.id;
  return null;
}

function markDeleted(out: Transaction, from: number, to: number, id: string, ctx: SuggestContext) {
  const doc = out.doc;
  const del = doc.type.schema.marks.deletion!.create({ id, author: ctx.author, at: ctx.now });
  const marks: [number, number][] = [];
  const removals: [number, number][] = [];
  const attrs: [number, NodeSuggestion][] = [];
  doc.nodesBetween(from, to, (node, pos) => {
    if (node.isInline) {
      const a = Math.max(pos, from);
      const b = Math.min(pos + node.nodeSize, to);
      if (b > a) {
        if (ownInsertion(node, ctx.author)) removals.push([a, b]);
        else if (!markOf(node, "deletion")) marks.push([a, b]);
      }
      return false;
    }
    if (pos < from) return true; // opened before the range: look inside
    const s = sugOf(node);
    if (pos + node.nodeSize <= to) {
      // The whole node.
      // A block you added, holding only what you added (a split keeps original text).
      if (onlyOwn(doc, pos, pos + node.nodeSize, ctx.author)) {
        removals.push([pos, pos + node.nodeSize]);
        return false;
      }
      // Other new blocks: mark what's inside instead.
      if (s?.kind === "insert") return true;
      if (s?.kind !== "delete") attrs.push([pos, { kind: "delete", id, author: ctx.author, at: ctx.now, ...(s?.from ? { from: s.from } : {}) }]);
      return false;
    }
    // Starts inside, ends after: the boundary before it is deleted.
    if (!s || s.kind === "change") attrs.push([pos, { kind: "join", id, author: ctx.author, at: ctx.now, ...(s?.from ? { from: s.from } : {}) }]);
    return true;
  });
  for (const [a, b] of marks) out.addMark(a, b, del);
  for (const [pos, v] of attrs) out.setNodeAttribute(pos, "suggestion", v);
  for (let i = removals.length - 1; i >= 0; i--) out.delete(removals[i]![0], removals[i]![1]);
}

function markInserted(out: Transaction, from: number, to: number, id: string, ctx: SuggestContext) {
  const schema = out.doc.type.schema;
  out.removeMark(from, to, schema.marks.deletion!);
  out.addMark(from, to, schema.marks.insertion!.create({ id, author: ctx.author, at: ctx.now }));
  const starts: number[] = [];
  out.doc.nodesBetween(from, to, (node, pos) => {
    if (node.isInline) return false;
    if (pos >= from) starts.push(pos);
    return true;
  });
  for (const pos of starts) {
    out.setNodeAttribute(pos, "suggestion", { kind: "insert", id, author: ctx.author, at: ctx.now } satisfies NodeSuggestion);
    // A split copies the source id; only the original block keeps it.
    if (out.doc.nodeAt(pos)?.attrs.sid != null) out.setNodeAttribute(pos, "sid", null);
  }
}

/**
 * Replays a replace step as a suggestion. Returns the index (among the steps
 * it added) of the step inserting the slice, when it mirrors the original.
 */
function suggestReplace(out: Transaction, step: ReplaceStep, map: Mapping, ctx: SuggestContext): { mirror: number | null } | null {
  const before = out.steps.length;
  const from = map.map(step.from, 1);
  const to = Math.max(from, map.map(step.to, -1));
  const slice = step.slice;
  if (to > from && onlyOwn(out.doc, from, to, ctx.author)) {
    // Taking back your own suggestion: a real edit.
    const mapped = step.map(map);
    if (!mapped || out.maybeStep(mapped).failed) return null;
    if (slice.size) {
      const id = adjacentId(out.doc, from, from + slice.size, "insertion", ctx.author) ?? ctx.newId();
      markInserted(out, from, from + slice.size, id, ctx);
    }
    return { mirror: slice.size ? 0 : null };
  }
  let id: string | null = null;
  if (to > from) {
    id = adjacentId(out.doc, from, to, "deletion", ctx.author) ?? ctx.newId();
    markDeleted(out, from, to, id, ctx);
  }
  if (!slice.size) return { mirror: null };
  const at = out.mapping.slice(before).map(to, 1);
  id ??= adjacentId(out.doc, at, at, "insertion", ctx.author) ?? ctx.newId();
  const ins = replaceStep(out.doc, at, at, slice);
  if (!ins || out.maybeStep(ins).failed) return null;
  let start = at;
  let end = at;
  ins.getMap().forEach((_os, _oe, ns, ne) => {
    start = ns;
    end = ne;
  });
  const mirror = ins instanceof ReplaceStep && end - start === slice.size ? out.steps.length - 1 - before : null;
  markInserted(out, start, end, id, ctx);
  return { mirror };
}

/** setBlockType / setNodeMarkup: one node replaced around its content. */
function isMarkupChange(s: ReplaceAroundStep, doc: PMNode): boolean {
  return s.insert === 1 && s.slice.size === 2 && s.gapFrom === s.from + 1 && s.gapTo === s.to - 1 && doc.nodeAt(s.from)?.nodeSize === s.to - s.from;
}

function suggestChange(out: Transaction, step: ReplaceAroundStep | AttrStep, map: Mapping, ctx: SuggestContext): boolean {
  if (step instanceof AttrStep && step.attr === "suggestion") return false;
  const mapped = step.map(map) as ReplaceAroundStep | AttrStep | null;
  if (!mapped) return true;
  const pos = mapped instanceof AttrStep ? mapped.pos : mapped.from;
  if (mapped instanceof ReplaceAroundStep && !isMarkupChange(mapped, out.doc)) return false;
  const old = out.doc.nodeAt(pos);
  if (!old || out.maybeStep(mapped).failed) return false;
  const now = out.doc.nodeAt(pos)!;
  const prev = sugOf(old);
  // Keep the source id: rejecting gives back the original block.
  if (old.attrs.sid != null && now.attrs.sid == null && "sid" in now.attrs) out.setNodeAttribute(pos, "sid", old.attrs.sid);
  const origin = prev?.from ?? { type: old.type.name, attrs: plainAttrs(old.attrs) };
  const same = canonical(origin) === canonical({ type: now.type.name, attrs: plainAttrs(now.attrs) });
  let value: NodeSuggestion | null;
  if (prev?.kind === "insert") value = prev; // a new block: the change is part of it
  else if (prev?.kind === "join" || prev?.kind === "delete") {
    // One suggestion per node: the pending join or deletion also carries the change.
    const rest: NodeSuggestion = { ...prev };
    delete rest.from;
    value = same ? rest : { ...rest, from: origin };
  } else {
    value = same ? null : { kind: "change", id: prev?.id ?? ctx.newId(), author: prev?.author ?? ctx.author, at: prev?.at ?? ctx.now, from: origin };
  }
  if (canonical(now.attrs.suggestion ?? null) !== canonical(value)) out.setNodeAttribute(pos, "suggestion", value);
  return true;
}

/** The transaction as a suggestion, or null when it can't be one. */
export function suggestTransaction(state: EditorState, tr: Transaction, ctx: SuggestContext): Transaction | null {
  const out = state.tr;
  let map = new Mapping();
  for (const step of tr.steps) {
    const before = out.steps.length;
    const next = new Mapping();
    next.appendMap(step.getMap().invert());
    next.appendMapping(map);
    let mirror: number | null = null;
    if (step instanceof ReplaceStep) {
      const r = suggestReplace(out, step, map, ctx);
      if (!r) return null;
      mirror = r.mirror;
    } else if (step instanceof ReplaceAroundStep || step instanceof AttrStep) {
      if (!suggestChange(out, step, map, ctx)) return null;
    } else {
      return null;
    }
    for (let k = before; k < out.steps.length; k++) next.appendMap(out.mapping.maps[k]!, k - before === mirror ? 0 : undefined);
    map = next;
  }
  // Selection: where the original put it; a plain Backspace or Delete moves
  // past the text it marked.
  let sel = tr.selection.map(out.doc, map);
  const only = tr.steps.length === 1 ? tr.steps[0] : null;
  if (only instanceof ReplaceStep && only.slice.size === 0 && state.selection.empty) {
    if (state.selection.head === only.to) sel = Selection.near(out.doc.resolve(out.mapping.map(only.from, -1)), -1);
    else if (state.selection.head === only.from) sel = Selection.near(out.doc.resolve(out.mapping.map(only.to, 1)), 1);
  }
  out.setSelection(sel);
  if (tr.storedMarksSet) out.setStoredMarks(tr.storedMarks);
  for (const [k, v] of Object.entries((tr as unknown as { meta: Record<string, unknown> }).meta)) out.setMeta(k, v);
  if (tr.scrolledIntoView) out.scrollIntoView();
  out.setTime(tr.time);
  return out;
}

declare module "@tiptap/core" {
  interface Storage {
    suggesting: { enabled: boolean };
  }
}

export type SuggestingOptions = { author: string; onRefused?: () => void };

/**
 * Rewrites the user's edits as suggestions while `editor.storage.suggesting.enabled`
 * is on. Remote changes and undo (both arrive through Yjs) pass through.
 */
export const Suggesting = Extension.create<SuggestingOptions, { enabled: boolean }>({
  name: "suggesting",
  addOptions: () => ({ author: "" }),
  addStorage: () => ({ enabled: false }),
  dispatchTransaction({ transaction, next }) {
    if (!this.storage.enabled || !transaction.docChanged || transaction.getMeta(ySyncPluginKey) || transaction.getMeta(DIRECT_EDIT)) {
      next(transaction);
      return;
    }
    const out = suggestTransaction(this.editor.state, transaction, { author: this.options.author, now: Date.now(), newId: newSuggestionId });
    if (out) next(out);
    else this.options.onRefused?.();
  },
});

/** Turns suggesting mode on or off for this editor. */
export function setSuggesting(editor: { storage: { suggesting: { enabled: boolean } } }, on: boolean) {
  editor.storage.suggesting.enabled = on;
}

export type EditorSuggestion = SuggestionInfo & { from: number; to: number };

/**
 * Pending suggestions in the editor's document, in order, with the range
 * each covers (for cards and jumping to them).
 */
export function editorSuggestions(doc: PMNode): EditorSuggestion[] {
  const byID = new Map<string, EditorSuggestion>();
  const get = (a: { id: string; author: string; at?: number }, from: number, to: number) => {
    let x = byID.get(a.id);
    if (!x) {
      x = { id: a.id, author: a.author, at: a.at, inserted: "", deleted: "", kinds: [], from, to };
      byID.set(a.id, x);
    }
    x.to = Math.max(x.to, to);
    return x;
  };
  const text = (n: PMNode) => (n.isText ? (n.text ?? "") : n.type.name === "hardBreak" ? "\n" : n.type.name === "image" ? "🖼" : "");
  doc.descendants((node, pos) => {
    if (node.isInline) {
      const ins = markOf(node, "insertion");
      if (ins) get(ins.attrs as never, pos, pos + node.nodeSize).inserted += text(node);
      const del = markOf(node, "deletion");
      if (del) get(del.attrs as never, pos, pos + node.nodeSize).deleted += text(node);
      return false;
    }
    const s = sugOf(node);
    if (s?.id) {
      const x = get(s, pos, pos + node.nodeSize);
      if (!x.kinds.includes(s.kind as SuggestionKind)) x.kinds.push(s.kind);
      if (s.kind === "change" && s.from) x.change = { from: s.from.type, to: node.type.name };
      if (s.kind === "delete") x.deleted += node.textContent;
    }
    return true;
  });
  return [...byID.values()];
}
