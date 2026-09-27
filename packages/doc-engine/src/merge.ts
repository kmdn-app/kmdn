/**
 * Three-way merge for updates from Published (docs/specs/06-git-and-forges.md#updates-from-published).
 *
 * Blocks are aligned by content hash (diff3 over block lists); where both
 * sides changed the same blocks differently, the chunk's source text is
 * merged line by line. What still clashes becomes a `conflict` node holding
 * both versions:
 *
 *   conflict { id }
 *     conflictSide { side: "published" }  ← theirs
 *     conflictSide { side: "revision" }   ← ours
 *
 * Until someone resolves it, the page materializes with the revision's side.
 */
import { nodeHash } from "./hash";
import { parse, type ParseResult } from "./parse";
import type { BlockNode, DocNode } from "./schema";

export type Chunk<T> = { kind: "same" | "clean"; items: T[] } | { kind: "conflict"; base: T[]; ours: T[]; theirs: T[] };

/** Longest common subsequence as index pairs (common prefix and suffix first). */
export function lcs(a: string[], b: string[]): [number, number][] {
  const n = a.length;
  const m = b.length;
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

type Hunk = { b0: number; b1: number; x0: number; x1: number; side: 0 | 1 };

/** Replaced ranges of base in a side, from the matched pairs. */
function hunksOf(pairs: [number, number][], nb: number, nx: number, side: 0 | 1): Hunk[] {
  const out: Hunk[] = [];
  let pb = 0;
  let px = 0;
  for (const [b, x] of [...pairs, [nb, nx] as [number, number]]) {
    if (b > pb || x > px) out.push({ b0: pb, b1: b, x0: px, x1: x, side });
    pb = b + 1;
    px = x + 1;
  }
  return out;
}

/**
 * Three-way merge of sequences: chunks that are unchanged ("same"), changed
 * on one side or identically on both ("clean", holding the result), or
 * changed differently on both ("conflict"). Only changes to overlapping
 * ranges of base clash (or two insertions at the same place): unlike git,
 * edits to neighbouring items merge, which suits documents.
 */
export function diff3<T>(base: T[], ours: T[], theirs: T[], key: (x: T) => string): Chunk<T>[] {
  const kb = base.map(key);
  const sides = [ours, theirs];
  const keys = [ours.map(key), theirs.map(key)];
  const pairs = [lcs(kb, keys[0]!), lcs(kb, keys[1]!)];
  const match = pairs.map((ps) => new Map(ps));
  const hunks = [...hunksOf(pairs[0]!, base.length, ours.length, 0), ...hunksOf(pairs[1]!, base.length, theirs.length, 1)].sort((a, b) => a.b0 - b.b0 || a.b1 - b.b1);
  // Group clashing hunks.
  const groups: { b0: number; b1: number; hs: Hunk[] }[] = [];
  for (const h of hunks) {
    const g = groups[groups.length - 1];
    if (g && (h.b0 < g.b1 || (g.b0 === g.b1 && h.b0 === g.b1 && h.b0 === h.b1))) {
      g.b1 = Math.max(g.b1, h.b1);
      g.hs.push(h);
    } else groups.push({ b0: h.b0, b1: h.b1, hs: [h] });
  }
  // Where a base boundary falls in a side.
  const start = (side: 0 | 1, b: number, hs: Hunk[]) => hs.find((h) => h.side === side && h.b0 === b)?.x0 ?? (b === base.length ? sides[side]!.length : match[side]!.get(b)!);
  const end = (side: 0 | 1, b: number, hs: Hunk[]) => hs.find((h) => h.side === side && h.b1 === b)?.x1 ?? (b === 0 ? 0 : match[side]!.get(b - 1)! + 1);
  const out: Chunk<T>[] = [];
  const same = (from: number, to: number) => {
    if (to <= from) return;
    const items = [];
    for (let i = from; i < to; i++) items.push(ours[match[0]!.get(i)!]!);
    const last = out[out.length - 1];
    if (last?.kind === "same") last.items.push(...items);
    else out.push({ kind: "same", items });
  };
  let at = 0;
  for (const g of groups) {
    same(at, g.b0);
    const bySide = new Set(g.hs.map((h) => h.side));
    if (bySide.size === 1) {
      const h = g.hs[0]!;
      out.push({ kind: "clean", items: sides[h.side]!.slice(h.x0, h.x1) });
    } else {
      const o = ours.slice(start(0, g.b0, g.hs), end(0, g.b1, g.hs));
      const t = theirs.slice(start(1, g.b0, g.hs), end(1, g.b1, g.hs));
      if (o.length === t.length && o.every((x, i) => key(x) === key(t[i]!))) out.push({ kind: "clean", items: o });
      else out.push({ kind: "conflict", base: base.slice(g.b0, g.b1), ours: o, theirs: t });
    }
    at = g.b1;
  }
  same(at, base.length);
  return out;
}

export type MergeResult = { doc: DocNode; conflicts: number };

function stripSid(b: BlockNode): BlockNode {
  const a = (b as { attrs?: Record<string, unknown> }).attrs;
  if (!a || !("sid" in a)) return b;
  const { sid: _sid, ...rest } = a;
  return { ...b, attrs: rest } as BlockNode;
}

/** The exact source text of consecutive top-level blocks. */
function sourceOf(r: ParseResult, blocks: BlockNode[]): string | null {
  if (!blocks.length) return "";
  const sid = (b: BlockNode) => (b as { attrs?: { sid?: number } }).attrs?.sid;
  const first = sid(blocks[0]!);
  const last = sid(blocks[blocks.length - 1]!);
  if (first === undefined || last === undefined) return null;
  const a = r.sourceMap.blocks[first];
  const b = r.sourceMap.blocks[last];
  return a && b ? r.sourceMap.original.slice(a.start, b.end) : null;
}

/**
 * Merges Published's changes (base → theirs) into the revision's page
 * (base → ours). Blocks from the revision keep their source ids, so the
 * result serializes with the revision's bytes where nothing changed.
 */
export function merge3(base: string, ours: string, theirs: string): MergeResult {
  const pb = parse(base);
  const po = parse(ours);
  const pt = parse(theirs);
  const theirsOnly = (bs: BlockNode[]) => bs.map(stripSid);
  const mine = new Set<BlockNode>(po.doc.content);
  const out: BlockNode[] = [];
  let conflicts = 0;
  for (const c of diff3(pb.doc.content, po.doc.content, pt.doc.content, nodeHash)) {
    if (c.kind !== "conflict") {
      // Clean chunks may come from Published: their source ids point elsewhere.
      out.push(...c.items.map((b) => (mine.has(b) ? b : stripSid(b))));
      continue;
    }
    // Same blocks changed on both sides: try line by line.
    const sb = sourceOf(pb, c.base);
    const so = sourceOf(po, c.ours);
    const st = sourceOf(pt, c.theirs);
    if (sb !== null && so !== null && st !== null) {
      const lines = diff3(sb.split("\n"), so.split("\n"), st.split("\n"), (l) => l);
      if (lines.every((x) => x.kind !== "conflict")) {
        const merged = lines.flatMap((x) => ("items" in x ? x.items : [])).join("\n");
        out.push(...parse(merged).doc.content.map(stripSid));
        continue;
      }
    }
    conflicts++;
    out.push({
      type: "conflict",
      attrs: { id: `c${conflicts}` },
      content: [
        { type: "conflictSide", attrs: { side: "published" }, content: theirsOnly(c.theirs) },
        { type: "conflictSide", attrs: { side: "revision" }, content: c.ours.map(stripSid) },
      ],
    } as unknown as BlockNode);
  }
  return { doc: { type: "doc", content: out }, conflicts };
}

type ConflictNode = { type: "conflict"; attrs?: { id?: string }; content?: { type: string; attrs?: { side?: string }; content?: BlockNode[] }[] };

export function hasConflicts(doc: DocNode): boolean {
  return doc.content.some((b) => (b.type as string) === "conflict");
}

/** The page with each unresolved conflict showing the revision's side (how it materializes). */
export function withoutConflicts(doc: DocNode): DocNode {
  if (!hasConflicts(doc)) return doc;
  const content: BlockNode[] = [];
  for (const b of doc.content) {
    if ((b.type as string) !== "conflict") content.push(b);
    else content.push(...((b as unknown as ConflictNode).content?.find((s) => s.attrs?.side === "revision")?.content ?? []));
  }
  return { ...doc, content };
}
