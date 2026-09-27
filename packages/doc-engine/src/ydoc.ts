import * as Y from "yjs";
import { sortMarks, type AnyNode, type DocNode, type Mark, type TextNode } from "./schema";

/**
 * Converts between the document model and a Y.XmlFragment using the same
 * encoding as y-prosemirror, so the server (which has no ProseMirror) and the
 * editor agree on the collaborative document:
 *
 * - element nodes → Y.XmlElement(type) with non-null attrs as attributes
 * - runs of text nodes → one Y.XmlText; marks are formatting attributes
 *   `{[markType]: markAttrs}`
 *
 * See docs/specs/05-collaboration.md#ydoc-layout.
 */

/** Name of the fragment holding the page. */
export const CONTENT = "content";

type Attrs = Record<string, unknown>;
type ElementJSON = { type: string; attrs?: Attrs; content?: AnyNode[] };

function isText(n: AnyNode): n is TextNode {
  return n.type === "text";
}

function markAttrs(marks: Mark[] | undefined): Attrs | undefined {
  if (!marks?.length) return undefined;
  const out: Attrs = {};
  for (const m of marks) out[m.type] = "attrs" in m ? m.attrs : {};
  return out;
}

function toY(node: ElementJSON): Y.XmlElement {
  const el = new Y.XmlElement(node.type);
  for (const [k, v] of Object.entries(node.attrs ?? {})) {
    if (v !== null && v !== undefined) el.setAttribute(k, v as string);
  }
  el.insert(0, children(node.content ?? []));
  return el;
}

function children(content: AnyNode[]): (Y.XmlElement | Y.XmlText)[] {
  const out: (Y.XmlElement | Y.XmlText)[] = [];
  for (let i = 0; i < content.length; i++) {
    const n = content[i]!;
    if (!isText(n)) {
      out.push(toY(n as ElementJSON));
      continue;
    }
    const run: TextNode[] = [];
    while (i < content.length && isText(content[i]!)) run.push(content[i++] as TextNode);
    i--;
    const t = new Y.XmlText();
    t.applyDelta(run.map((r) => ({ insert: r.text, attributes: markAttrs(r.marks) })));
    out.push(t);
  }
  return out;
}

/** Fills an empty fragment with the document. */
export function writeDoc(frag: Y.XmlFragment, doc: DocNode): void {
  frag.insert(0, children(doc.content));
}

/** Reads the fragment back into the document model. */
export function readDoc(frag: Y.XmlFragment): DocNode {
  return { type: "doc", content: readChildren(frag) as DocNode["content"] };
}

function readChildren(parent: Y.XmlFragment | Y.XmlElement): AnyNode[] {
  const out: AnyNode[] = [];
  for (const c of parent.toArray()) {
    if (c instanceof Y.XmlText) {
      for (const op of c.toDelta() as { insert: unknown; attributes?: Attrs }[]) {
        if (typeof op.insert !== "string" || op.insert === "") continue;
        const t: TextNode = { type: "text", text: op.insert };
        const marks: Mark[] = [];
        for (const [k, v] of Object.entries(op.attributes ?? {})) {
          const type = k.replace(/--.*$/, ""); // y-prosemirror's overlapping-mark keys
          if (type === "ychange") continue;
          marks.push((v && typeof v === "object" && Object.keys(v).length ? { type, attrs: v } : { type }) as Mark);
        }
        if (marks.length) t.marks = sortMarks(marks);
        // Yjs splits runs at formatting boundaries even when the marks end up
        // the same: merge them, as ProseMirror does, so hashes stay stable.
        const prev = out[out.length - 1];
        if (prev && isText(prev) && canonicalMarks(prev.marks) === canonicalMarks(t.marks)) out[out.length - 1] = { ...prev, text: prev.text + t.text };
        else out.push(t);
      }
    } else if (c instanceof Y.XmlElement) {
      // Always an attrs object: absent attributes were null (see canonical).
      const node: ElementJSON = { type: c.nodeName, attrs: c.getAttributes() as Attrs };
      const kids = readChildren(c);
      if (kids.length) node.content = kids;
      out.push(node as AnyNode);
    }
  }
  return out;
}

const TEXTBLOCKS = new Set(["paragraph", "heading"]);

function stripSids(n: AnyNode): AnyNode {
  const e = n as ElementJSON;
  if (!e.attrs || !("sid" in e.attrs)) return n;
  const { sid: _sid, ...rest } = e.attrs;
  return { ...e, attrs: rest } as AnyNode;
}

/** Longest common subsequence of two hash lists, as index pairs. */
function lcs(a: string[], b: string[]): [number, number][] {
  const n = a.length;
  const m = b.length;
  // Common prefix/suffix first: most edits are local.
  let pre = 0;
  while (pre < n && pre < m && a[pre] === b[pre]) pre++;
  let suf = 0;
  while (suf < n - pre && suf < m - pre && a[n - 1 - suf] === b[m - 1 - suf]) suf++;
  const out: [number, number][] = [];
  for (let i = 0; i < pre; i++) out.push([i, i]);
  const A = a.slice(pre, n - suf);
  const B = b.slice(pre, m - suf);
  if (A.length && B.length && A.length * B.length <= 4_000_000) {
    const dp: Uint32Array[] = Array.from({ length: A.length + 1 }, () => new Uint32Array(B.length + 1));
    for (let i = A.length - 1; i >= 0; i--)
      for (let j = B.length - 1; j >= 0; j--) dp[i]![j] = A[i] === B[j] ? dp[i + 1]![j + 1]! + 1 : Math.max(dp[i + 1]![j]!, dp[i]![j + 1]!);
    let i = 0;
    let j = 0;
    while (i < A.length && j < B.length) {
      if (A[i] === B[j]) {
        out.push([pre + i, pre + j]);
        i++;
        j++;
      } else if (dp[i + 1]![j]! >= dp[i]![j + 1]!) i++;
      else j++;
    }
  }
  for (let k = 0; k < suf; k++) out.push([n - suf + k, m - suf + k]);
  return out;
}

type Run = { ch: string; key: string; attrs?: Attrs };

function runs(content: AnyNode[] | undefined): Run[] | null {
  const out: Run[] = [];
  for (const n of content ?? []) {
    if (!isText(n)) return null;
    const attrs = markAttrs(n.marks);
    const key = canonicalMarks(n.marks);
    for (const ch of n.text) out.push({ ch, key, attrs });
  }
  return out;
}

function canonicalMarks(marks: Mark[] | undefined): string {
  return JSON.stringify(sortMarks(marks ?? []));
}

/**
 * Edits a text block's single Y.XmlText in place: keeps the common prefix and
 * suffix (text and marks) and replaces the middle, so concurrent edits and
 * comment anchors elsewhere in the paragraph survive.
 */
function patchText(t: Y.XmlText, from: Run[], to: Run[]): void {
  let pre = 0;
  while (pre < from.length && pre < to.length && from[pre]!.ch === to[pre]!.ch && from[pre]!.key === to[pre]!.key) pre++;
  let suf = 0;
  while (suf < from.length - pre && suf < to.length - pre && from[from.length - 1 - suf]!.ch === to[to.length - 1 - suf]!.ch && from[from.length - 1 - suf]!.key === to[to.length - 1 - suf]!.key) suf++;
  // Y.XmlText lengths count UTF-16 code units, runs count code points.
  const len = (rs: Run[]) => rs.reduce((s, r) => s + r.ch.length, 0);
  const at = len(from.slice(0, pre));
  const oldMid = from.slice(pre, from.length - suf);
  const mid = to.slice(pre, to.length - suf);
  // Same text, other marks (a suggestion accepted or rejected, formatting):
  // reformat in place so the text keeps its authorship.
  if (oldMid.length === mid.length && oldMid.every((r, i) => r.ch === mid[i]!.ch)) {
    let pos = at;
    for (let i = 0; i < mid.length; ) {
      let j = i;
      let n = 0;
      while (j < mid.length && mid[j]!.key === mid[i]!.key && oldMid[j]!.key === oldMid[i]!.key) n += mid[j++]!.ch.length;
      if (mid[i]!.key !== oldMid[i]!.key) {
        const attrs: Attrs = { ...(mid[i]!.attrs ?? {}) };
        for (const k of Object.keys(oldMid[i]!.attrs ?? {})) if (!(k in attrs)) attrs[k] = null;
        t.format(pos, n, attrs);
      }
      pos += n;
      i = j;
    }
    return;
  }
  const del = len(oldMid);
  if (del) t.delete(at, del);
  let pos = at;
  for (let i = 0; i < mid.length; ) {
    let j = i;
    let s = "";
    while (j < mid.length && mid[j]!.key === mid[i]!.key) s += mid[j++]!.ch;
    // With attributes given, Yjs clears the left neighbour's other marks.
    t.insert(pos, s, { ...(mid[i]!.attrs ?? {}) });
    pos += s.length;
    i = j;
  }
}

/** Sets the element's attributes to attrs (the source id aside). */
function syncAttrs(el: Y.XmlElement, attrs: Attrs | undefined): void {
  const want = new Map(sortedAttrs(attrs));
  for (const [k] of Object.entries(el.getAttributes())) if (k !== "sid" && !want.has(k)) el.removeAttribute(k);
  const have = el.getAttributes() as Attrs;
  for (const [k, v] of want) if (canonicalValue(have[k]) !== canonicalValue(v)) el.setAttribute(k, v as string);
}

function canonicalValue(v: unknown): string {
  return JSON.stringify(v, (_k, x: unknown) => (x && typeof x === "object" && !Array.isArray(x) ? Object.fromEntries(Object.entries(x as Attrs).sort(([a], [b]) => (a < b ? -1 : 1))) : x)) ?? "null";
}

/** Containers whose children are diffed recursively when their type and attrs match. */
const CONTAINERS = new Set(["bulletList", "orderedList", "listItem", "blockquote", "footnoteDefinition", "table", "tableRow"]);
/** Nodes holding only inline content, patched in place. */
const INLINE_HOLDERS = new Set([...TEXTBLOCKS, "tableCell", "tableHeader", "codeBlock", "mathBlock"]);

/**
 * Turns the fragment into `target` with a minimal set of operations:
 * unchanged nodes (by content hash) are left alone, same-shaped text blocks
 * are patched in place, same-shaped containers (lists, quotes, tables) are
 * diffed recursively, and everything else is deleted and re-inserted. Used
 * for edits made outside an editor (the assistant, applying updates from
 * Published, restoring a checkpoint, source mode), so concurrent edits,
 * comment anchors and authorship elsewhere survive.
 */
export function applyDoc(frag: Y.XmlFragment, target: DocNode, hash: (n: unknown) => string): void {
  applyChildren(frag, readDoc(frag).content as AnyNode[], target.content as AnyNode[], hash, true);
}

function applyChildren(parent: Y.XmlFragment | Y.XmlElement, current: AnyNode[], next: AnyNode[], hash: (n: unknown) => string, top: boolean): void {
  const ha = current.map(hash);
  const hb = next.map(hash);
  const common = lcs(ha, hb);
  // Walk gaps between matched pairs from the end so indices stay valid.
  const anchors: [number, number][] = [[-1, -1], ...common, [current.length, next.length]];
  for (let g = anchors.length - 1; g > 0; g--) {
    const [a0, b0] = anchors[g - 1]!;
    const [a1, b1] = anchors[g]!;
    const oldIdx = a0 + 1;
    const oldCount = a1 - oldIdx;
    const repl = next.slice(b0 + 1, b1);
    // Pair up same-shaped nodes one-to-one from the start of the gap.
    let k = 0;
    while (k < oldCount && k < repl.length) {
      const o = current[oldIdx + k] as ElementJSON;
      const n = repl[k] as ElementJSON;
      const el = parent.get(oldIdx + k);
      if (!(el instanceof Y.XmlElement) || o.type !== n.type) break;
      // Same type, other attributes (a heading level, a suggestion resolved):
      // updated in place when the children can be patched too.
      const sameAttrs = JSON.stringify(sortedAttrs(o.attrs)) === JSON.stringify(sortedAttrs(n.attrs));
      if (INLINE_HOLDERS.has(o.type)) {
        const from = runs(o.content);
        const to = runs(n.content);
        if (!from || !to || el.length > 1 || (el.length === 1 && !(el.get(0) instanceof Y.XmlText))) break;
        if (!sameAttrs) syncAttrs(el, n.attrs);
        let t = el.get(0) as Y.XmlText | undefined;
        if (!t) {
          t = new Y.XmlText();
          el.insert(0, [t]);
        }
        patchText(t, from, to);
      } else if (CONTAINERS.has(o.type)) {
        if (!sameAttrs) syncAttrs(el, n.attrs);
        applyChildren(el, o.content ?? [], n.content ?? [], hash, false);
      } else if (!sameAttrs && !o.content && !n.content) {
        syncAttrs(el, n.attrs);
      } else {
        break;
      }
      k++;
    }
    if (oldCount - k > 0) parent.delete(oldIdx + k, oldCount - k);
    const inserts = (top ? repl.slice(k).map(stripSids) : repl.slice(k)) as ElementJSON[];
    if (inserts.length) parent.insert(oldIdx + k, inserts.map(toY));
  }
}

function sortedAttrs(a: Attrs | undefined): [string, unknown][] {
  return Object.entries(a ?? {})
    .filter(([k, v]) => k !== "sid" && v !== null && v !== undefined)
    .sort(([x], [y]) => (x < y ? -1 : 1));
}
