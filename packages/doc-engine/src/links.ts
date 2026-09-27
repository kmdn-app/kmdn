import type * as md from "mdast";
import { toMdast } from "./parse";

/**
 * Link extraction and byte-preserving rewriting for the link index, the
 * broken-link checker and rename rewriting (docs/specs/04-doc-engine.md#link-index-and-graph).
 */

export type LinkKind = "link" | "image" | "definition";

export type Link = {
  kind: LinkKind;
  /** The destination as written (decoded). */
  url: string;
  /** Byte range of the destination in the source (UTF-16 offsets), for rewriting. -1 when not rewritable. */
  start: number;
  end: number;
  /** 1-based line of the link. */
  line: number;
  /** Destination was written in angle brackets. */
  bracketed: boolean;
};

export type Heading = { text: string; depth: number; slug: string; line: number };

/** Punctuation and symbols outside ASCII that GitHub drops from anchors. */
function droppedNonASCII(cp: number): boolean {
  return (
    (cp >= 0x00a0 && cp <= 0x00bf) || cp === 0x00d7 || cp === 0x00f7 || (cp >= 0x2000 && cp <= 0x206f) || (cp >= 0x20a0 && cp <= 0x2bff) ||
    (cp >= 0x3000 && cp <= 0x303f) || (cp >= 0xfe30 && cp <= 0xfe4f) || (cp >= 0xff00 && cp <= 0xff0f) || (cp >= 0x1f000 && cp <= 0x1faff)
  );
}

/**
 * GitHub-style heading anchor: lowercase; letters, digits, "-" and "_" kept;
 * whitespace becomes "-". Written without Unicode property escapes so it
 * behaves the same in browsers and in the Go host (goja lacks \p{…}).
 */
export function slugify(text: string): string {
  let out = "";
  for (const ch of text.toLowerCase().trim()) {
    const cp = ch.codePointAt(0)!;
    if ((cp >= 0x61 && cp <= 0x7a) || (cp >= 0x30 && cp <= 0x39) || ch === "-" || ch === "_") out += ch;
    else if (ch === " " || ch === "\t" || ch === "\n") out += "-";
    else if (cp > 0x7f && !droppedNonASCII(cp)) out += ch;
  }
  return out;
}

function textOf(n: md.Nodes): string {
  if ("value" in n && typeof n.value === "string") return n.value;
  if ("children" in n) return (n.children as md.Nodes[]).map(textOf).join("");
  return "";
}

/** Parses a link destination at src[i]; returns its end and value. */
function destinationAt(src: string, i: number): { end: number; value: string; bracketed: boolean } | null {
  while (src[i] === " " || src[i] === "\t" || src[i] === "\n" || src[i] === "\r") i++;
  if (src[i] === "<") {
    const close = src.indexOf(">", i + 1);
    if (close < 0 || src.slice(i + 1, close).includes("\n")) return null;
    return { end: close + 1, value: src.slice(i + 1, close), bracketed: true };
  }
  let depth = 0;
  let j = i;
  for (; j < src.length; j++) {
    const c = src[j]!;
    if (c === "\\") {
      j++;
      continue;
    }
    if (c === " " || c === "\t" || c === "\n" || c === "\r") break;
    if (c === "(") depth++;
    else if (c === ")") {
      if (depth === 0) break;
      depth--;
    }
  }
  return j > i ? { end: j, value: src.slice(i, j), bracketed: false } : null;
}

const decode = (s: string) => {
  try {
    return decodeURI(s.replace(/\\([!-/:-@[-`{-~])/g, "$1"));
  } catch {
    return s;
  }
};

/** Links, images and reference definitions in document order, plus headings. */
export function extractLinks(markdown: string): { links: Link[]; headings: Heading[] } {
  // Offsets index the original text (micromark keeps \r\n), so rewrites are byte-exact.
  const src = markdown;
  const tree = toMdast(src);
  const links: Link[] = [];
  const headings: Heading[] = [];
  const seen = new Map<string, number>();
  const visit = (n: md.Nodes) => {
    const pos = n.position;
    if (n.type === "heading" && pos) {
      const text = textOf(n);
      let slug = slugify(text);
      const count = seen.get(slug) ?? 0;
      seen.set(slug, count + 1);
      if (count > 0) slug = `${slug}-${count}`;
      headings.push({ text, depth: n.depth, slug, line: pos.start.line });
    }
    if ((n.type === "link" || n.type === "image" || n.type === "definition") && pos && pos.start.offset !== undefined && pos.end.offset !== undefined) {
      const s = pos.start.offset;
      const e = pos.end.offset;
      const kind: LinkKind = n.type;
      let found: { start: number; end: number; bracketed: boolean } | null = null;
      if (kind === "definition") {
        const colon = src.indexOf("]:", s);
        const d = colon >= 0 && colon < e ? destinationAt(src, colon + 2) : null;
        if (d) found = { start: d.end - d.value.length - (d.bracketed ? 1 : 0), end: d.bracketed ? d.end - 1 : d.end, bracketed: d.bracketed };
      } else {
        // The destination follows the last "](" whose destination decodes to the node's url.
        for (let j = src.lastIndexOf("](", e); j >= s; j = src.lastIndexOf("](", j - 1)) {
          const d = destinationAt(src, j + 2);
          if (d && decode(d.value) === decode(n.url)) {
            const start = d.end - d.value.length - (d.bracketed ? 1 : 0);
            found = { start, end: d.bracketed ? d.end - 1 : d.end, bracketed: d.bracketed };
            break;
          }
          if (j === 0) break;
        }
      }
      links.push({ kind, url: n.url, start: found?.start ?? -1, end: found?.end ?? -1, line: pos.start.line, bracketed: found?.bracketed ?? false });
    }
    if ("children" in n) for (const c of n.children as md.Nodes[]) visit(c);
  };
  visit(tree);
  return { links, headings };
}

/** Encodes a destination for markdown (spaces and parentheses need care unless bracketed). */
function encodeDest(url: string, bracketed: boolean): string {
  if (bracketed) return url.replace(/[<>\n]/g, (c) => encodeURIComponent(c));
  return url.replace(/[ ()<>]/g, (c) => encodeURIComponent(c));
}

/**
 * Replaces link destinations: `rewrite` gets each destination and returns a
 * new one (or null to keep it). Only the destination bytes change.
 */
export function rewriteLinks(markdown: string, rewrite: (url: string, link: Link) => string | null): string {
  const src = markdown;
  const { links } = extractLinks(src);
  let out = "";
  let last = 0;
  for (const l of links) {
    if (l.start < 0) continue;
    const next = rewrite(l.url, l);
    if (next === null || next === l.url) continue;
    out += src.slice(last, l.start) + encodeDest(next, l.bracketed);
    last = l.end;
  }
  return out + src.slice(last);
}
