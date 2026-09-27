/**
 * Suggestions (tracked changes), see docs/specs/05-collaboration.md#suggestions-tracked-changes.
 *
 * In the document they are:
 * - `insertion` / `deletion` marks `{id, author, at}` on inline content;
 * - a `suggestion` attribute `{kind, id, author, at, from?}` on nodes:
 *   - `insert`: the node was added (a paragraph split, a pasted block);
 *     rejecting it drops it, or joins what's left into the previous node;
 *   - `join`: accepting joins the node into the previous one (a deletion
 *     across a block boundary);
 *   - `delete`: accepting removes the node;
 *   - `change`: the node's type or attributes changed; `from` holds the
 *     previous `{type, attrs}` (a join or delete can carry one too, when the
 *     node had a pending change).
 *
 * Pending suggestions are not applied: the markdown a page materializes to
 * (for diffs and publishing) is the document with every suggestion rejected.
 */
import { canonical, nodeHash } from "./hash";
import type { AnyNode, DocNode, Mark } from "./schema";

export type SuggestionKind = "insert" | "join" | "delete" | "change";
export type SuggestionAttrs = { id: string; author: string; at?: number };
export type NodeSuggestion = SuggestionAttrs & { kind: SuggestionKind; from?: { type: string; attrs?: Record<string, unknown> } };

export type Decision = "accept" | "reject" | null;
export type Decide = (s: SuggestionAttrs) => Decision;

type Attrs = Record<string, unknown>;
type El = { type: string; attrs?: Attrs; content?: AnyNode[]; text?: string; marks?: Mark[] };

const TEXT_JOINABLE = new Set(["paragraph", "heading"]);

function markOf(n: El, type: string): SuggestionAttrs | null {
  const m = n.marks?.find((x) => x.type === (type as Mark["type"]));
  return m && "attrs" in m ? (m.attrs as unknown as SuggestionAttrs) : null;
}

function withoutMark(n: El, type: string): El {
  const marks = n.marks?.filter((m) => m.type !== (type as Mark["type"]));
  const out = { ...n };
  if (marks?.length) out.marks = marks;
  else delete out.marks;
  return out;
}

function withoutSuggestion(attrs: Attrs | undefined): Attrs | undefined {
  if (!attrs || !("suggestion" in attrs)) return attrs;
  const { suggestion: _s, ...rest } = attrs;
  return rest;
}

/** Nodes with inline content (leaves included), as opposed to block containers. */
function isInlineNode(n: El): boolean {
  return n.type === "text" || n.type === "hardBreak" || n.type === "image" || n.type === "footnoteReference" || n.type === "mathInline" || n.type === "rawInline";
}

function empty(n: El): boolean {
  return !n.content || n.content.length === 0;
}

/** Nodes holding inline content. */
const TEXTBLOCKS = new Set(["paragraph", "heading", "codeBlock", "mathBlock", "tableCell", "tableHeader"]);

/** Joins b into a (both already resolved), like ProseMirror's join; false when they don't fit. */
function joinInto(a: El, b: El): boolean {
  if (TEXTBLOCKS.has(a.type) && TEXTBLOCKS.has(b.type)) {
    if (a.type !== b.type && !(TEXT_JOINABLE.has(a.type) && TEXT_JOINABLE.has(b.type))) return false;
    a.content = mergeText([...(a.content ?? []), ...(b.content ?? [])]);
    return true;
  }
  if (a.type === b.type && !TEXTBLOCKS.has(a.type) && !isLeafBlock(a)) {
    const kids = [...(b.content ?? [])] as El[];
    const last = a.content?.[a.content.length - 1] as El | undefined;
    const first = kids[0];
    a.content = [...(a.content ?? [])];
    if (last && first && joinInto(last, first)) kids.shift();
    a.content.push(...(kids as AnyNode[]));
    return true;
  }
  return false;
}

function sameMarks(a: El, b: El): boolean {
  return canonical(a.marks ?? []) === canonical(b.marks ?? []);
}

/** Merges adjacent text nodes with the same marks (as ProseMirror does), so hashes match parsed documents. */
function mergeText(content: AnyNode[]): AnyNode[] {
  const out: El[] = [];
  for (const n of content as El[]) {
    const prev = out[out.length - 1];
    if (n.type === "text" && prev?.type === "text" && sameMarks(prev, n)) out[out.length - 1] = { ...prev, text: (prev.text ?? "") + (n.text ?? "") };
    else if (n.type !== "text" || n.text) out.push(n);
  }
  return out as AnyNode[];
}

function resolveContent(content: AnyNode[], decide: Decide): AnyNode[] {
  const out: El[] = [];
  for (const raw of content as El[]) {
    if (isInlineNode(raw)) {
      let n = raw;
      const ins = markOf(n, "insertion");
      if (ins) {
        const d = decide(ins);
        if (d === "reject") continue;
        if (d === "accept") n = withoutMark(n, "insertion");
      }
      const del = markOf(n, "deletion");
      if (del) {
        const d = decide(del);
        if (d === "accept") continue;
        if (d === "reject") n = withoutMark(n, "deletion");
      }
      out.push(n);
      continue;
    }
    const node: El = { ...raw };
    if (raw.content) node.content = resolveContent(raw.content, decide);
    const s = raw.attrs?.suggestion as NodeSuggestion | undefined;
    const d = s ? decide(s) : null;
    if (!s || !d) {
      out.push(node);
      continue;
    }
    node.attrs = withoutSuggestion(node.attrs);
    const prev = out[out.length - 1];
    // Undoing a split or accepting a cross-block deletion: what's left of the
    // node joins the previous one.
    const join = () => {
      if (isLeafBlock(raw) || empty(node)) return;
      if (!(prev && joinInto(prev, node))) out.push(node);
    };
    // Any node suggestion may also record a type change (`from`): rejecting reverts it.
    if (d === "reject" && s.from && s.kind !== "insert") {
      const sid = node.attrs?.sid;
      node.type = s.from.type;
      node.attrs = { ...(s.from.attrs ?? {}) };
      if (sid !== undefined) node.attrs.sid = sid;
    }
    if (s.kind === "insert") {
      if (d === "reject") join();
      else out.push(node);
    } else if (s.kind === "join") {
      if (d === "accept") join();
      else out.push(node);
    } else if (s.kind === "delete") {
      if (d === "reject") out.push(node);
    } else out.push(node);
  }
  return mergeText(out as AnyNode[]);
}

/** Block nodes without children in the model (rules, front matter, raw blocks). */
function isLeafBlock(n: El): boolean {
  return n.type === "horizontalRule" || n.type === "frontmatter" || n.type === "rawBlock";
}

/**
 * Applies decisions to suggestions: accepted ones become plain content,
 * rejected ones are undone, the others stay as they are.
 */
export function resolveSuggestions(doc: DocNode, decide: Decide): DocNode {
  return { ...doc, content: resolveContent(doc.content, decide) as DocNode["content"] };
}

/** The document as it would be with every pending suggestion rejected. */
export function withoutSuggestions(doc: DocNode): DocNode {
  return hasSuggestions(doc) ? resolveSuggestions(doc, () => "reject") : doc;
}

export function hasSuggestions(node: unknown): boolean {
  const n = node as El;
  if (n.marks?.some((m) => (m.type as string) === "insertion" || (m.type as string) === "deletion")) return true;
  if (n.attrs?.suggestion) return true;
  return !!n.content?.some(hasSuggestions);
}

export type SuggestionInfo = {
  id: string;
  author: string;
  at?: number;
  /** Text added and removed, for the card ("Replace “a” with “b”"). */
  inserted: string;
  deleted: string;
  /** Node-level kinds involved. */
  kinds: SuggestionKind[];
  /** For a change: the previous and new node type. */
  change?: { from: string; to: string };
};

/** Pending suggestions in document order. */
export function listSuggestions(doc: DocNode): SuggestionInfo[] {
  const byID = new Map<string, SuggestionInfo>();
  const get = (s: SuggestionAttrs): SuggestionInfo => {
    let x = byID.get(s.id);
    if (!x) {
      x = { id: s.id, author: s.author, at: s.at, inserted: "", deleted: "", kinds: [] };
      byID.set(s.id, x);
    }
    return x;
  };
  const text = (n: El): string => (n.type === "text" ? (n.text ?? "") : n.type === "hardBreak" ? "\n" : n.type === "image" ? "🖼" : "");
  const walk = (n: El) => {
    if (isInlineNode(n)) {
      const ins = markOf(n, "insertion");
      if (ins) get(ins).inserted += text(n);
      const del = markOf(n, "deletion");
      if (del) get(del).deleted += text(n);
      return;
    }
    const s = n.attrs?.suggestion as NodeSuggestion | undefined;
    if (s?.id) {
      const x = get(s);
      if (!x.kinds.includes(s.kind)) x.kinds.push(s.kind);
      if (s.from && s.from.type !== n.type) x.change = { from: s.from.type, to: n.type };
      if (s.kind === "delete") x.deleted += plain(n);
    }
    n.content?.forEach((c) => walk(c as El));
  };
  const plain = (n: El): string => (isInlineNode(n) ? text(n) : (n.content ?? []).map((c) => plain(c as El)).join(" "));
  walk(doc as unknown as El);
  return [...byID.values()];
}

/**
 * Hash of a node as published (pending suggestions rejected): edits made
 * outside the editor diff against this, so blocks they don't touch keep
 * their suggestions.
 */
export function settledHash(node: unknown): string {
  if (!hasSuggestions(node)) return nodeHash(node);
  const [n] = resolveContent([node as AnyNode], () => "reject");
  return nodeHash(n ?? null);
}
