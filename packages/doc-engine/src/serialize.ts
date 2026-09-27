import { toMarkdown, type Options } from "mdast-util-to-markdown";
import { gfmToMarkdown } from "mdast-util-gfm";
import { frontmatterToMarkdown } from "mdast-util-frontmatter";
import { mathToMarkdown } from "mdast-util-math";
import type * as md from "mdast";
import { nodeHash } from "./hash";
import { MARK_ORDER, defaultStyle, type BlockNode, type DocNode, type InlineNode, type Mark, type SourceMap, type TextNode, type Style } from "./schema";

/**
 * Serializes a document to markdown. With a source map, every top-level block
 * whose content is unchanged is emitted with its original bytes, and the
 * whitespace between consecutive original blocks is kept, so untouched parts
 * of a file never change. See docs/specs/04-doc-engine.md#markdown-fidelity-the-core-requirement.
 */
export function serialize(doc: DocNode, sourceMap?: SourceMap): string {
  const style = sourceMap?.style ?? defaultStyle;
  const nl = style.lineEnding;
  const blocks = doc.content;
  let out = sourceMap ? sourceMap.leading : "";
  let prevSid: number | undefined;
  blocks.forEach((b, i) => {
    const sid = (b as { attrs?: { sid?: number } }).attrs?.sid;
    const orig = sourceMap && sid !== undefined ? sourceMap.blocks[sid] : undefined;
    const reuse = orig && orig.hash === nodeHash(b);
    if (i > 0) {
      if (sourceMap && prevSid !== undefined && sid !== undefined && sid === prevSid + 1) {
        out += sourceMap.original.slice(sourceMap.blocks[prevSid]!.end, sourceMap.blocks[sid]!.start);
      } else {
        out += nl + nl;
      }
    }
    out += reuse ? sourceMap!.original.slice(orig.start, orig.end) : serializeBlock(b, style);
    prevSid = sid;
  });
  if (sourceMap) {
    out += blocks.length ? sourceMap.trailing : "";
    if (!blocks.length && !sourceMap.blocks.length) out = sourceMap.leading;
  } else if (blocks.length) {
    out += nl;
  }
  return sourceMap?.bom ? "﻿" + out : out;
}

/** Serializes a single block with the given style, without a trailing newline. */
export function serializeBlock(b: BlockNode, style: Style = defaultStyle): string {
  let s: string;
  if (b.type === "frontmatter") {
    const fence = b.attrs.format === "toml" ? "+++" : "---";
    s = `${fence}\n${b.attrs.value}\n${fence}`;
  } else if (b.type === "rawBlock") {
    s = b.attrs.raw;
  } else {
    s = toMarkdown({ type: "root", children: [toMdastBlock(b)] } as md.Root, options(style)).replace(/\n+$/, "");
  }
  return style.lineEnding === "\r\n" ? s.replace(/\r?\n/g, "\r\n") : s;
}

function options(style: Style): Options {
  return {
    bullet: style.bullet,
    bulletOther: style.bullet === "-" ? "*" : "-",
    emphasis: style.emphasis,
    strong: style.strong,
    fence: style.fence,
    fences: true,
    rule: style.rule,
    listItemIndent: style.listItemIndent,
    setext: style.setext,
    incrementListMarker: true,
    extensions: [gfmToMarkdown(), frontmatterToMarkdown(["yaml", "toml"]), mathToMarkdown()],
  };
}

/** Converts a block of the model to mdast. Raw blocks become html nodes (emitted verbatim). */
export function toMdastBlock(b: BlockNode): md.RootContent {
  switch (b.type) {
    case "paragraph":
      return { type: "paragraph", children: toPhrasing(b.content ?? []) };
    case "heading":
      return { type: "heading", depth: Math.min(Math.max(b.attrs.level, 1), 6) as md.Heading["depth"], children: toPhrasing(b.content ?? []) };
    case "blockquote":
      return { type: "blockquote", children: b.content.map(toMdastBlock) as md.BlockContent[] };
    case "bulletList":
    case "orderedList":
      return {
        type: "list",
        ordered: b.type === "orderedList",
        start: b.type === "orderedList" ? b.attrs.start : null,
        spread: b.attrs.spread,
        children: b.content.map((li) => ({
          type: "listItem",
          checked: li.attrs.checked,
          spread: li.attrs.spread,
          children: li.content.map(toMdastBlock) as md.BlockContent[],
        })),
      };
    case "codeBlock":
      return { type: "code", lang: b.attrs.lang, meta: b.attrs.meta, value: (b.content ?? []).map((t) => t.text).join("") };
    case "mathBlock":
      return { type: "math", value: (b.content ?? []).map((t) => t.text).join("") } as md.RootContent;
    case "table":
      return {
        type: "table",
        align: b.attrs.align,
        children: b.content.map((row) => ({
          type: "tableRow",
          children: row.content.map((cell) => ({ type: "tableCell", children: toPhrasing(cell.content ?? []) })),
        })),
      };
    case "horizontalRule":
      return { type: "thematicBreak" };
    case "footnoteDefinition":
      return { type: "footnoteDefinition", identifier: b.attrs.label.toLowerCase(), label: b.attrs.label, children: b.content.map(toMdastBlock) as md.BlockContent[] };
    case "frontmatter":
      return { type: "yaml", value: b.attrs.value };
    case "rawBlock":
      return { type: "html", value: b.attrs.raw };
  }
}

const FLANKING: Mark["type"][] = ["bold", "italic", "strike"];

/**
 * Moves whitespace at the edges of bold/italic/strike runs outside the mark.
 * Markdown can't express "** bold**" (the delimiter must touch the text), and
 * editors produce it all the time: toggle bold, type a space, type a word.
 */
export function hoistWhitespace(nodes: InlineNode[]): InlineNode[] {
  const emph = (n: InlineNode | undefined) => new Set(n?.type === "text" ? (n.marks ?? []).filter((m) => FLANKING.includes(m.type)).map((m) => m.type) : []);
  const out: InlineNode[] = [];
  nodes.forEach((n, i) => {
    if (n.type !== "text" || !n.marks?.length) {
      out.push(n);
      return;
    }
    const mine = emph(n);
    const prev = emph(nodes[i - 1]);
    const next = emph(nodes[i + 1]);
    const opening = [...mine].filter((m) => !prev.has(m));
    const closing = [...mine].filter((m) => !next.has(m));
    const without = (drop: string[]) => n.marks!.filter((m) => !drop.includes(m.type));
    const piece = (text: string, marks: Mark[]): TextNode => (marks.length ? { type: "text", text, marks } : { type: "text", text });
    let text = n.text;
    const lead = opening.length ? (/^\s+/.exec(text)?.[0] ?? "") : "";
    if (lead === text) {
      out.push(piece(text, without([...opening, ...closing])));
      return;
    }
    const trail = closing.length ? (/\s+$/.exec(text)?.[0] ?? "") : "";
    text = text.slice(lead.length, text.length - trail.length);
    if (lead) out.push(piece(lead, without(opening)));
    if (text) out.push(piece(text, n.marks));
    if (trail) out.push(piece(trail, without(closing)));
  });
  return out;
}

/** Rebuilds nested mdast phrasing from flat marked text. */
export function toPhrasing(input: InlineNode[]): md.PhrasingContent[] {
  const nodes = hoistWhitespace(input);
  type Frame = { mark: Mark | null; children: md.PhrasingContent[] };
  const root: Frame = { mark: null, children: [] };
  let stack: Frame[] = [root];
  const key = (m: Mark) => (m.type === "link" ? `link:${m.attrs.href}:${m.attrs.title ?? ""}:${m.attrs.ref ?? ""}` : m.type);

  for (const n of nodes) {
    const marks = n.type === "text" ? [...(n.marks ?? [])].filter((m) => m.type !== "code").sort((a, b) => MARK_ORDER.indexOf(a.type) - MARK_ORDER.indexOf(b.type)) : [];
    // Keep the longest prefix of open frames that matches this node's marks.
    let depth = 1;
    while (depth < stack.length && depth - 1 < marks.length && key(stack[depth]!.mark!) === key(marks[depth - 1]!)) depth++;
    stack = stack.slice(0, depth);
    for (let i = depth - 1; i < marks.length; i++) {
      const frame: Frame = { mark: marks[i]!, children: [] };
      stack[stack.length - 1]!.children.push(wrap(frame));
      stack.push(frame);
    }
    stack[stack.length - 1]!.children.push(...leaf(n));
  }
  return root.children;

  function wrap(f: Frame): md.PhrasingContent {
    const m = f.mark!;
    // The node object is created now and its children array filled later.
    switch (m.type) {
      case "bold":
        return { type: "strong", children: f.children };
      case "italic":
        return { type: "emphasis", children: f.children };
      case "strike":
        return { type: "delete", children: f.children };
      case "link":
        if (m.attrs.ref) return { type: "linkReference", identifier: m.attrs.ref.toLowerCase(), label: m.attrs.ref, referenceType: "full", children: f.children };
        return { type: "link", url: m.attrs.href, title: m.attrs.title, children: f.children };
      default:
        return { type: "text", value: "" };
    }
  }
}

function leaf(n: InlineNode): md.PhrasingContent[] {
  switch (n.type) {
    case "text":
      return (n.marks ?? []).some((m) => m.type === "code") ? [{ type: "inlineCode", value: n.text }] : [{ type: "text", value: n.text }];
    case "hardBreak":
      return [{ type: "break" }];
    case "image":
      if (n.attrs.ref) return [{ type: "imageReference", identifier: n.attrs.ref.toLowerCase(), label: n.attrs.ref, alt: n.attrs.alt, referenceType: "full" }];
      return [{ type: "image", url: n.attrs.src, alt: n.attrs.alt, title: n.attrs.title }];
    case "footnoteReference":
      return [{ type: "footnoteReference", identifier: n.attrs.label.toLowerCase(), label: n.attrs.label }];
    case "mathInline":
      return [{ type: "inlineMath", value: n.attrs.source } as md.PhrasingContent];
    case "rawInline":
      return [{ type: "html", value: n.attrs.raw }];
  }
}
