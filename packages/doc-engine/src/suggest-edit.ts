/**
 * Turns an edit into suggestions: given the page as it is (with any pending
 * suggestions) and the page someone wants, returns the page with the
 * difference as insertion/deletion marks and node suggestions, so the edit
 * can be reviewed like a person's (docs/specs/08-assistant.md#tools, edit_file).
 *
 * Blocks are aligned by their published (settled) content; blocks both sides
 * have are kept as they are, suggestions included. Changed text blocks are
 * diffed word by word; blocks only the target has are inserted
 * and blocks only the current page has are marked deleted.
 */
import { canonical, nodeHash } from "./hash";
import { lcs } from "./merge";
import type { AnyNode, DocNode, Mark } from "./schema";
import { resolveSuggestions, settledHash, type NodeSuggestion, type SuggestionAttrs } from "./suggestions";

export type EditAttrs = SuggestionAttrs & { assistant?: boolean };

type El = { type: string; attrs?: Record<string, unknown>; content?: AnyNode[]; text?: string; marks?: Mark[] };

const INLINE = new Set(["text", "hardBreak", "image", "footnoteReference", "mathInline", "rawInline"]);
const TEXTBLOCKS = new Set(["paragraph", "heading", "codeBlock", "mathBlock", "tableCell", "tableHeader"]);
const CONTAINERS = new Set(["bulletList", "orderedList", "listItem", "blockquote", "table", "tableRow", "footnoteDefinition"]);

/** Limit for the word diff inside a block (cells of the LCS table). */
const MAX_CELLS = 4_000_000;

function settle(n: El): El {
  const d = resolveSuggestions({ type: "doc", content: [n as AnyNode] } as DocNode, () => "reject");
  return (d.content[0] as unknown as El) ?? { ...n, content: [] };
}

function markAttrs(a: EditAttrs) {
  return { id: a.id, author: a.author, ...(a.at ? { at: a.at } : {}), ...(a.assistant ? { assistant: true } : {}) };
}

function withMark(n: El, type: "insertion" | "deletion", a: EditAttrs): El {
  const marks = (n.marks ?? []).filter((m) => m.type !== type);
  return { ...n, marks: [...marks, { type, attrs: markAttrs(a) } as Mark] };
}

function nodeSuggestion(kind: NodeSuggestion["kind"], a: EditAttrs, from?: NodeSuggestion["from"]): NodeSuggestion {
  return { kind, ...markAttrs(a), ...(from ? { from } : {}) } as NodeSuggestion;
}

/** A node the target adds: every inline marked inserted, every node marked insert. */
function inserted(n: El, a: EditAttrs): El {
  if (INLINE.has(n.type)) return withMark(n, "insertion", a);
  const out: El = { ...n, attrs: { ...(n.attrs ?? {}), suggestion: nodeSuggestion("insert", a) } };
  if (out.attrs && "sid" in out.attrs) delete out.attrs.sid;
  if (n.content) out.content = n.content.map((c) => inserted(c as El, a) as AnyNode);
  return out;
}

/** A node the target drops: a delete suggestion on it (accepting removes it). */
function deleted(n: El, a: EditAttrs): El {
  const s = n.attrs?.suggestion as NodeSuggestion | undefined;
  if (s?.kind === "delete") return n;
  return { ...n, attrs: { ...(n.attrs ?? {}), suggestion: nodeSuggestion("delete", a, s?.from) } };
}

type Tok = { node: El; key: string };

// Word characters: ASCII letters and digits, and anything beyond ASCII
// (no \p{L} in every JS host the engine runs in).
const wordChar = (c: number) => (c >= 48 && c <= 57) || (c >= 65 && c <= 90) || (c >= 97 && c <= 122) || c === 95 || c > 127;

/** Splits text into words, runs of spaces, and single other characters. */
function words(text: string): string[] {
  const out: string[] = [];
  let i = 0;
  while (i < text.length) {
    const c = text.charCodeAt(i);
    let j = i + 1;
    if (wordChar(c)) while (j < text.length && wordChar(text.charCodeAt(j))) j++;
    else if (c === 32 || c === 9) while (j < text.length && (text.charCodeAt(j) === 32 || text.charCodeAt(j) === 9)) j++;
    out.push(text.slice(i, j));
    i = j;
  }
  return out;
}

/** Inline content as tokens: words of text (with their marks), and atoms. */
function tokens(content: AnyNode[] | undefined): Tok[] {
  const out: Tok[] = [];
  for (const raw of (content ?? []) as El[]) {
    if (raw.type === "text") {
      const key = canonical(raw.marks ?? []);
      for (const w of words(raw.text ?? "")) out.push({ node: { ...raw, text: w }, key: "t" + w + key });
    } else {
      out.push({ node: raw, key: "n" + canonical(raw) });
    }
  }
  return out;
}

function mergeText(nodes: El[]): AnyNode[] {
  const out: El[] = [];
  for (const n of nodes) {
    const prev = out[out.length - 1];
    if (n.type === "text" && prev?.type === "text" && canonical(prev.marks ?? []) === canonical(n.marks ?? [])) out[out.length - 1] = { ...prev, text: (prev.text ?? "") + (n.text ?? "") };
    else out.push(n);
  }
  return out.map((n) => (n.marks && !n.marks.length ? (({ marks: _m, ...rest }) => rest)(n) : n)) as AnyNode[];
}

/** The target's inline content against the current one, as suggestions. */
function inlineDiff(cur: AnyNode[] | undefined, tgt: AnyNode[] | undefined, a: EditAttrs): AnyNode[] {
  const x = tokens(cur);
  const y = tokens(tgt);
  let pre = 0;
  while (pre < x.length && pre < y.length && x[pre]!.key === y[pre]!.key) pre++;
  let suf = 0;
  while (suf < x.length - pre && suf < y.length - pre && x[x.length - 1 - suf]!.key === y[y.length - 1 - suf]!.key) suf++;
  const xm = x.slice(pre, x.length - suf);
  const ym = y.slice(pre, y.length - suf);
  const out: El[] = x.slice(0, pre).map((t) => t.node);
  const emit = (dels: Tok[], ins: Tok[]) => {
    for (const t of dels) out.push(withMark(t.node, "deletion", a));
    for (const t of ins) out.push(withMark(t.node, "insertion", a));
  };
  if (xm.length * ym.length > MAX_CELLS) emit(xm, ym);
  else {
    const pairs = lcs(
      xm.map((t) => t.key),
      ym.map((t) => t.key),
    );
    let i = 0;
    let j = 0;
    for (const [pi, pj] of [...pairs, [xm.length, ym.length] as [number, number]]) {
      emit(xm.slice(i, pi), ym.slice(j, pj));
      if (pi < xm.length) out.push(xm[pi]!.node);
      i = pi + 1;
      j = pj + 1;
    }
  }
  out.push(...x.slice(x.length - suf).map((t) => t.node));
  return mergeText(out);
}

function plainAttrs(attrs: Record<string, unknown> | undefined) {
  const { suggestion: _s, sid: _sid, ...rest } = attrs ?? {};
  return rest;
}

/** A changed node both sides have (same type): patch its content in place. */
function paired(cur: El, tgt: El, a: EditAttrs): El {
  const settled = settle(cur);
  const out: El = { ...tgt, attrs: { ...(tgt.attrs ?? {}) } };
  if (cur.attrs?.sid !== undefined) out.attrs!.sid = cur.attrs.sid;
  // An attribute change (a heading level): a change suggestion holding the old one.
  if (canonical(plainAttrs(settled.attrs)) !== canonical(plainAttrs(tgt.attrs))) out.attrs!.suggestion = nodeSuggestion("change", a, { type: settled.type, attrs: plainAttrs(settled.attrs) });
  if (TEXTBLOCKS.has(cur.type)) out.content = inlineDiff(settled.content, tgt.content, a);
  else if (tgt.content || settled.content) out.content = children(settled.content ?? [], tgt.content ?? [], a);
  if (out.content && !out.content.length) delete out.content;
  return out;
}

function pairable(c: El, t: El): boolean {
  return c.type === t.type && (TEXTBLOCKS.has(c.type) || CONTAINERS.has(c.type));
}

function children(cur: AnyNode[], tgt: AnyNode[], a: EditAttrs): AnyNode[] {
  const cs = cur as El[];
  const ts = tgt as El[];
  const pairs = lcs(cs.map(settledHash), ts.map(nodeHash));
  const out: El[] = [];
  let i = 0;
  let j = 0;
  for (const [pi, pj] of [...pairs, [cs.length, ts.length] as [number, number]]) {
    const olds = cs.slice(i, pi);
    const news = ts.slice(j, pj);
    let k = 0;
    // Same-shaped nodes at the same place in the gap are edits of each other.
    while (k < olds.length && k < news.length && pairable(olds[k]!, news[k]!)) {
      out.push(paired(olds[k]!, news[k]!, a));
      k++;
    }
    for (const o of olds.slice(k)) out.push(deleted(o, a));
    for (const n of news.slice(k)) out.push(inserted(n, a));
    if (pi < cs.length) out.push(cs[pi]!);
    i = pi + 1;
    j = pj + 1;
  }
  return out as AnyNode[];
}

/** The current page with the change to target recorded as suggestions. */
export function suggestEdit(current: DocNode, target: DocNode, attrs: EditAttrs): DocNode {
  return { type: "doc", content: children(current.content as AnyNode[], target.content as AnyNode[], attrs) as DocNode["content"] };
}
