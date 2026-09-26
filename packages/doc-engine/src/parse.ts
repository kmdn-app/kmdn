import { fromMarkdown } from "mdast-util-from-markdown";
import { gfmFromMarkdown } from "mdast-util-gfm";
import { frontmatterFromMarkdown } from "mdast-util-frontmatter";
import { mathFromMarkdown } from "mdast-util-math";
import { gfm } from "micromark-extension-gfm";
import { frontmatter } from "micromark-extension-frontmatter";
import { math } from "micromark-extension-math";
import type * as md from "mdast";
import { nodeHash } from "./hash";
import {
  defaultStyle,
  type Align,
  type BlockNode,
  type DocNode,
  type InlineNode,
  type ListItemNode,
  type Mark,
  sortMarks,
  type RawKind,
  type SourceMap,
  type Style,
  type TableCellNode,
  type TableRowNode,
} from "./schema";

export type ParseResult = { doc: DocNode; sourceMap: SourceMap };

const FM_TYPES: ("yaml" | "toml")[] = ["yaml", "toml"];

/** Parses markdown to mdast with the extensions kmdn supports. */
export function toMdast(src: string): md.Root {
  return fromMarkdown(src, {
    extensions: [gfm(), frontmatter(FM_TYPES), math()],
    mdastExtensions: [gfmFromMarkdown(), frontmatterFromMarkdown(FM_TYPES), mathFromMarkdown()],
  });
}

const lf = (s: string) => s.replace(/\r\n?/g, "\n");

/** Parses markdown into a document and the source map used for byte-faithful serialization. */
export function parse(markdown: string): ParseResult {
  const bom = markdown.charCodeAt(0) === 0xfeff;
  const src = bom ? markdown.slice(1) : markdown;
  const root = toMdast(src);
  const defs = collectDefinitions(root);
  const ctx: Ctx = { src, defs };

  // Top-level segments, merging ::: containers into one raw block.
  type Seg = { start: number; end: number; node: BlockNode };
  const segs: Seg[] = [];
  const kids = root.children;
  for (let i = 0; i < kids.length; i++) {
    const child = kids[i]!;
    const start = child.position!.start.offset!;
    let end = child.position!.end.offset!;
    const raw = src.slice(start, end);
    if (child.type === "paragraph" && /^:{3,}/.test(raw)) {
      let j = i;
      while (j < kids.length && !/(^|\n):{3,}\s*$/.test(src.slice(kids[j]!.position!.start.offset!, kids[j]!.position!.end.offset!).trimEnd())) j++;
      if (j < kids.length && (j > i || /\n:{3,}\s*$/.test(raw.trimEnd()))) {
        end = kids[j]!.position!.end.offset!;
        segs.push({ start, end, node: rawBlock(src.slice(start, end), "container") });
        i = j;
        continue;
      }
    }
    segs.push({ start, end, node: block(child, ctx) });
  }

  const blocks: SourceMap["blocks"] = [];
  const content: BlockNode[] = segs.map((seg, sid) => {
    const node = { ...seg.node, attrs: { ...(seg.node as { attrs?: object }).attrs, sid } } as BlockNode;
    blocks.push({ start: seg.start, end: seg.end, hash: nodeHash(node) });
    return node;
  });
  const leading = segs.length ? src.slice(0, segs[0]!.start) : src;
  const trailing = segs.length ? src.slice(segs[segs.length - 1]!.end) : "";
  return {
    doc: { type: "doc", content },
    sourceMap: { original: src, blocks, leading, trailing, style: detectStyle(root, src), bom },
  };
}

type Ctx = { src: string; defs: Map<string, { url: string; title: string | null }> };

function collectDefinitions(root: md.Root) {
  const defs = new Map<string, { url: string; title: string | null }>();
  const walk = (n: md.Nodes) => {
    if (n.type === "definition") defs.set(n.identifier, { url: n.url, title: n.title ?? null });
    if ("children" in n) for (const c of n.children) walk(c as md.Nodes);
  };
  walk(root);
  return defs;
}

function slice(ctx: Ctx, n: md.Node): string {
  return ctx.src.slice(n.position!.start.offset!, n.position!.end.offset!);
}

function rawBlock(raw: string, kind: RawKind): BlockNode {
  return { type: "rawBlock", attrs: { raw: lf(raw), kind } };
}

const SHORTCODE = /^\s*(\{\{[<%][\s\S]*[>%]\}\}|\{%[\s\S]*%\})\s*$/;
const ALERT = /^\[!(NOTE|TIP|IMPORTANT|WARNING|CAUTION)\]/i;

function textOf(n: md.Nodes): string {
  if ("value" in n && typeof n.value === "string") return n.value;
  if ("children" in n) return (n.children as md.Nodes[]).map(textOf).join("");
  return "";
}

function block(n: md.RootContent, ctx: Ctx): BlockNode {
  switch (n.type) {
    case "paragraph": {
      const raw = slice(ctx, n);
      if (SHORTCODE.test(raw)) return rawBlock(raw, "shortcode");
      return { type: "paragraph", content: inlines(n.children, ctx, []) };
    }
    case "heading":
      return { type: "heading", attrs: { level: n.depth }, content: inlines(n.children, ctx, []) };
    case "blockquote": {
      const first = n.children[0];
      if (first?.type === "paragraph" && ALERT.test(textOf(first))) return rawBlock(slice(ctx, n), "alert");
      return { type: "blockquote", content: blocks(n.children, ctx) };
    }
    case "list": {
      const items = n.children.map((li) => listItem(li, ctx));
      return n.ordered
        ? { type: "orderedList", attrs: { start: n.start ?? 1, spread: !!n.spread }, content: items }
        : { type: "bulletList", attrs: { spread: !!n.spread }, content: items };
    }
    case "code": {
      const value = lf(n.value);
      return { type: "codeBlock", attrs: { lang: n.lang ?? null, meta: n.meta ?? null }, content: value ? [{ type: "text", text: value }] : [] };
    }
    case "math": {
      const value = lf(n.value);
      return { type: "mathBlock", content: value ? [{ type: "text", text: value }] : [] };
    }
    case "table":
      return {
        type: "table",
        attrs: { align: (n.align ?? []).map((a) => (a ?? null) as Align) },
        content: n.children.map((row, i): TableRowNode => ({
          type: "tableRow",
          content: row.children.map((cell): TableCellNode => ({ type: i === 0 ? "tableHeader" : "tableCell", content: inlines(cell.children, ctx, []) })),
        })),
      };
    case "thematicBreak":
      return { type: "horizontalRule" };
    case "footnoteDefinition":
      return { type: "footnoteDefinition", attrs: { label: n.label ?? n.identifier }, content: blocks(n.children, ctx) };
    case "yaml":
      return { type: "frontmatter", attrs: { format: "yaml", value: lf(n.value) } };
    case "html":
      return rawBlock(slice(ctx, n), "html");
    case "definition":
      return rawBlock(slice(ctx, n), "definition");
    default: {
      const t = (n as md.Node).type;
      if (t === "toml") return { type: "frontmatter", attrs: { format: "toml", value: lf((n as md.Literal).value) } };
      return rawBlock(slice(ctx, n), "unknown");
    }
  }
}

function blocks(children: md.RootContent[], ctx: Ctx): BlockNode[] {
  return children.map((c) => block(c, ctx));
}

function listItem(li: md.ListItem, ctx: Ctx): ListItemNode {
  const content = blocks(li.children, ctx);
  return { type: "listItem", attrs: { checked: li.checked ?? null, spread: !!li.spread }, content: content.length ? content : [{ type: "paragraph" }] };
}

function addMark(marks: Mark[], m: Mark): Mark[] {
  return sortMarks([...marks.filter((x) => x.type !== m.type), m]);
}

function inlines(children: md.PhrasingContent[], ctx: Ctx, marks: Mark[]): InlineNode[] {
  const out: InlineNode[] = [];
  const text = (t: string, ms: Mark[]) => {
    if (!t) return;
    const prev = out[out.length - 1];
    if (prev && prev.type === "text" && sameMarks(prev.marks ?? [], ms)) {
      prev.text += t;
      return;
    }
    out.push(ms.length ? { type: "text", text: t, marks: ms } : { type: "text", text: t });
  };
  for (const c of children) {
    switch (c.type) {
      case "text":
        text(lf(c.value), marks);
        break;
      case "emphasis":
        out.push(...merge(out, inlines(c.children, ctx, addMark(marks, { type: "italic" }))));
        break;
      case "strong":
        out.push(...merge(out, inlines(c.children, ctx, addMark(marks, { type: "bold" }))));
        break;
      case "delete":
        out.push(...merge(out, inlines(c.children, ctx, addMark(marks, { type: "strike" }))));
        break;
      case "inlineCode":
        text(c.value, addMark(marks, { type: "code" }));
        break;
      case "link":
        out.push(...merge(out, inlines(c.children, ctx, addMark(marks, { type: "link", attrs: { href: c.url, title: c.title ?? null, ref: null } }))));
        break;
      case "linkReference": {
        const def = ctx.defs.get(c.identifier);
        if (!def) {
          out.push({ type: "rawInline", attrs: { raw: slice(ctx, c) } });
          break;
        }
        out.push(...merge(out, inlines(c.children, ctx, addMark(marks, { type: "link", attrs: { href: def.url, title: def.title, ref: c.label ?? c.identifier } }))));
        break;
      }
      case "image":
        out.push({ type: "image", attrs: { src: c.url, alt: c.alt ?? "", title: c.title ?? null } });
        break;
      case "imageReference": {
        const def = ctx.defs.get(c.identifier);
        if (!def) {
          out.push({ type: "rawInline", attrs: { raw: slice(ctx, c) } });
          break;
        }
        out.push({ type: "image", attrs: { src: def.url, alt: c.alt ?? "", title: def.title, ref: c.label ?? c.identifier } });
        break;
      }
      case "html":
        out.push({ type: "rawInline", attrs: { raw: slice(ctx, c) } });
        break;
      case "break":
        out.push({ type: "hardBreak" });
        break;
      case "footnoteReference":
        out.push({ type: "footnoteReference", attrs: { label: c.label ?? c.identifier } });
        break;
      case "inlineMath":
        out.push({ type: "mathInline", attrs: { source: c.value } });
        break;
      default:
        out.push({ type: "rawInline", attrs: { raw: slice(ctx, c as md.Node) } });
    }
  }
  return out;
}

// Merges adjacent same-mark text across nested calls (e.g. "*a*" "*b*").
function merge(out: InlineNode[], add: InlineNode[]): InlineNode[] {
  const first = add[0];
  const prev = out[out.length - 1];
  if (first && prev && first.type === "text" && prev.type === "text" && sameMarks(first.marks ?? [], prev.marks ?? [])) {
    prev.text += first.text;
    return add.slice(1);
  }
  return add;
}

function markKey(m: Mark): string {
  return m.type === "link" ? `link:${m.attrs.href}:${m.attrs.title ?? ""}:${m.attrs.ref ?? ""}` : m.type;
}

export function sameMarks(a: Mark[], b: Mark[]): boolean {
  if (a.length !== b.length) return false;
  const ka = a.map(markKey).sort();
  const kb = b.map(markKey).sort();
  return ka.every((k, i) => k === kb[i]);
}

function detectStyle(root: md.Root, src: string): Style {
  const style: Style = { ...defaultStyle };
  const crlf = (src.match(/\r\n/g) ?? []).length;
  const lfOnly = (src.match(/(^|[^\r])\n/g) ?? []).length;
  style.lineEnding = crlf > lfOnly ? "\r\n" : "\n";
  const seen = new Set<string>();
  let setextVotes = 0;
  let atxVotes = 0;
  const at = (n: md.Node) => src.charAt(n.position!.start.offset!);
  const walk = (n: md.Nodes) => {
    switch (n.type) {
      case "listItem":
        if (!seen.has("bullet")) {
          const c = at(n);
          if (c === "-" || c === "*" || c === "+") {
            style.bullet = c;
            seen.add("bullet");
          }
        }
        break;
      case "emphasis":
        if (!seen.has("em")) {
          const c = at(n);
          if (c === "*" || c === "_") style.emphasis = c;
          seen.add("em");
        }
        break;
      case "strong":
        if (!seen.has("strong")) {
          const c = at(n);
          if (c === "*" || c === "_") style.strong = c;
          seen.add("strong");
        }
        break;
      case "code":
        if (!seen.has("fence")) {
          const c = at(n);
          if (c === "`" || c === "~") {
            style.fence = c;
            seen.add("fence");
          }
        }
        break;
      case "thematicBreak":
        if (!seen.has("rule")) {
          const c = at(n);
          if (c === "-" || c === "*" || c === "_") style.rule = c;
          seen.add("rule");
        }
        break;
      case "heading":
        if (n.depth <= 2) {
          if (at(n) === "#") atxVotes++;
          else setextVotes++;
        }
        break;
    }
    if ("children" in n) for (const c of n.children as md.Nodes[]) walk(c);
  };
  walk(root);
  style.setext = setextVotes > atxVotes;
  return style;
}
