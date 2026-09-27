import { useEffect } from "react";

/**
 * Discussions on published pages anchor by quote, W3C TextQuoteSelector
 * style: the selected text plus a little context on each side, so the right
 * occurrence is found again (docs/specs/07-review.md#doc-discussions-published-docs).
 */
export type QuoteAnchor = { quote: string; prefix?: string; suffix?: string };

const CONTEXT = 32;

type TextIndex = { text: string; nodes: Text[]; starts: number[] };

function textIndex(root: HTMLElement): TextIndex {
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
  const nodes: Text[] = [];
  const starts: number[] = [];
  let text = "";
  for (let n = walker.nextNode(); n; n = walker.nextNode()) {
    starts.push(text.length);
    nodes.push(n as Text);
    text += (n as Text).data;
  }
  return { text, nodes, starts };
}

function point(ix: TextIndex, offset: number): [Text, number] | null {
  let lo = 0;
  let hi = ix.starts.length - 1;
  while (lo < hi) {
    const mid = (lo + hi + 1) >> 1;
    if (ix.starts[mid]! <= offset) lo = mid;
    else hi = mid - 1;
  }
  const n = ix.nodes[lo];
  return n ? [n, Math.min(offset - ix.starts[lo]!, n.data.length)] : null;
}

/** The current selection inside root as an anchor, or null. */
export function selectionQuote(root: HTMLElement | null): QuoteAnchor | null {
  const sel = window.getSelection();
  if (!root || !sel || sel.isCollapsed || !sel.rangeCount) return null;
  const r = sel.getRangeAt(0);
  if (!root.contains(r.commonAncestorContainer)) return null;
  const before = document.createRange();
  before.setStart(root, 0);
  before.setEnd(r.startContainer, r.startOffset);
  const start = before.toString().length;
  const quote = r.toString();
  if (!quote.trim()) return null;
  const text = textIndex(root).text;
  return { quote: quote.slice(0, 2000), prefix: text.slice(Math.max(0, start - CONTEXT), start), suffix: text.slice(start + quote.length, start + quote.length + CONTEXT) };
}

const sharedSuffix = (a: string, b: string) => {
  let n = 0;
  while (n < a.length && n < b.length && a[a.length - 1 - n] === b[b.length - 1 - n]) n++;
  return n;
};
const sharedPrefix = (a: string, b: string) => {
  let n = 0;
  while (n < a.length && n < b.length && a[n] === b[n]) n++;
  return n;
};

/** Where the quote is in text: the occurrence whose context matches best. */
export function findQuote(text: string, a: QuoteAnchor): [number, number] | null {
  if (!a.quote) return null;
  let best = -1;
  let score = -1;
  for (let i = text.indexOf(a.quote); i >= 0; i = text.indexOf(a.quote, i + 1)) {
    const end = i + a.quote.length;
    const s = (a.prefix ? sharedSuffix(text.slice(Math.max(0, i - a.prefix.length), i), a.prefix) : 0) + (a.suffix ? sharedPrefix(text.slice(end, end + a.suffix.length), a.suffix) : 0);
    if (s > score) {
      best = i;
      score = s;
    }
  }
  return best < 0 ? null : [best, best + a.quote.length];
}

const supported = () => typeof CSS !== "undefined" && "highlights" in CSS && typeof Highlight !== "undefined";

/**
 * Highlights anchored passages in root (without touching its DOM: CSS Custom
 * Highlights) and reports clicks on them. `version` changes when root's
 * content does.
 */
export function useQuoteHighlights(root: HTMLElement | null, items: { id: string; anchor: QuoteAnchor }[], active: string | null, onClick: (id: string) => void, version: unknown) {
  useEffect(() => {
    if (!root || !supported()) return;
    const ix = textIndex(root);
    const ranges = new Map<string, Range>();
    for (const it of items) {
      const at = findQuote(ix.text, it.anchor);
      const a = at && point(ix, at[0]);
      const b = at && point(ix, at[1]);
      if (!a || !b) continue;
      const r = document.createRange();
      r.setStart(a[0], a[1]);
      r.setEnd(b[0], b[1]);
      ranges.set(it.id, r);
    }
    CSS.highlights.set("kmdn-comment", new Highlight(...[...ranges].filter(([id]) => id !== active).map(([, r]) => r)));
    const act = active ? ranges.get(active) : undefined;
    if (act) CSS.highlights.set("kmdn-comment-active", new Highlight(act));
    else CSS.highlights.delete("kmdn-comment-active");
    const click = (e: MouseEvent) => {
      if (!window.getSelection()?.isCollapsed) return; // selecting, not clicking
      const doc = document as Document & { caretPositionFromPoint?: (x: number, y: number) => { offsetNode: Node; offset: number } | null };
      const p = doc.caretPositionFromPoint?.(e.clientX, e.clientY);
      const r = p ? null : document.caretRangeFromPoint?.(e.clientX, e.clientY);
      const node = p?.offsetNode ?? r?.startContainer;
      const offset = p?.offset ?? r?.startOffset ?? 0;
      if (!node) return;
      for (const [id, range] of ranges) {
        if (range.isPointInRange(node, offset)) {
          onClick(id);
          return;
        }
      }
    };
    root.addEventListener("click", click);
    return () => {
      root.removeEventListener("click", click);
      CSS.highlights.delete("kmdn-comment");
      CSS.highlights.delete("kmdn-comment-active");
    };
  }, [root, items, active, onClick, version]);
}
