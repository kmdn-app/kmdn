/**
 * Document model: ProseMirror-compatible JSON. One node type per markdown
 * construct so documents round-trip. See docs/specs/04-doc-engine.md#schema.
 *
 * Top-level blocks carry `attrs.sid` (source id) linking them to the byte
 * range they were parsed from; the serializer reuses those bytes verbatim while
 * the block is unchanged.
 */

export type Mark =
  | { type: "bold" }
  | { type: "italic" }
  | { type: "strike" }
  | { type: "code" }
  | { type: "link"; attrs: { href: string; title: string | null; ref: string | null } }
  // Suggestions (see suggestions.ts); never in parsed or serialized markdown.
  | { type: "insertion"; attrs: { id: string; author: string; at?: number } }
  | { type: "deletion"; attrs: { id: string; author: string; at?: number } };

export type TextNode = { type: "text"; text: string; marks?: Mark[] };

/** Canonical mark order: marks on a text node are always stored in this order. */
export const MARK_ORDER: Mark["type"][] = ["link", "bold", "italic", "strike", "code", "insertion", "deletion"];

export function sortMarks(marks: Mark[]): Mark[] {
  return [...marks].sort((a, b) => MARK_ORDER.indexOf(a.type) - MARK_ORDER.indexOf(b.type));
}

export type InlineNode =
  | TextNode
  | { type: "hardBreak" }
  | { type: "image"; attrs: { src: string; alt: string; title: string | null; ref?: string | null } }
  | { type: "footnoteReference"; attrs: { label: string } }
  | { type: "mathInline"; attrs: { source: string } }
  | { type: "rawInline"; attrs: { raw: string } };

export type Align = "left" | "center" | "right" | null;

export type BlockNode =
  | { type: "paragraph"; attrs?: BlockAttrs; content?: InlineNode[] }
  | { type: "heading"; attrs: BlockAttrs & { level: number }; content?: InlineNode[] }
  | { type: "blockquote"; attrs?: BlockAttrs; content: BlockNode[] }
  | { type: "bulletList"; attrs: BlockAttrs & { spread: boolean }; content: ListItemNode[] }
  | { type: "orderedList"; attrs: BlockAttrs & { start: number; spread: boolean }; content: ListItemNode[] }
  | { type: "codeBlock"; attrs: BlockAttrs & { lang: string | null; meta: string | null }; content?: TextNode[] }
  | { type: "mathBlock"; attrs?: BlockAttrs; content?: TextNode[] }
  | { type: "table"; attrs: BlockAttrs & { align: Align[] }; content: TableRowNode[] }
  | { type: "horizontalRule"; attrs?: BlockAttrs }
  | { type: "footnoteDefinition"; attrs: BlockAttrs & { label: string }; content: BlockNode[] }
  | { type: "frontmatter"; attrs: BlockAttrs & { format: "yaml" | "toml"; value: string } }
  | { type: "rawBlock"; attrs: BlockAttrs & { raw: string; kind: RawKind } };

export type RawKind = "html" | "shortcode" | "definition" | "alert" | "container" | "unknown";

export type ListItemNode = { type: "listItem"; attrs: { checked: boolean | null; spread: boolean }; content: BlockNode[] };
export type TableRowNode = { type: "tableRow"; content: TableCellNode[] };
export type TableCellNode = { type: "tableHeader" | "tableCell"; content?: InlineNode[] };

export type BlockAttrs = { sid?: number };

export type DocNode = { type: "doc"; content: BlockNode[] };

export type AnyNode = DocNode | BlockNode | ListItemNode | TableRowNode | TableCellNode | InlineNode;

/** Markdown style conventions detected per file, used for changed blocks. */
export type Style = {
  bullet: "-" | "*" | "+";
  emphasis: "*" | "_";
  strong: "*" | "_";
  fence: "`" | "~";
  rule: "-" | "*" | "_";
  listItemIndent: "one" | "tab" | "mixed";
  setext: boolean;
  lineEnding: "\n" | "\r\n";
};

export const defaultStyle: Style = {
  bullet: "-",
  emphasis: "*",
  strong: "*",
  fence: "`",
  rule: "-",
  listItemIndent: "one",
  setext: false,
  lineEnding: "\n",
};

/**
 * SourceMap links a parsed document to its original text. It lives outside the
 * collaborative document (server-side per revision file, and in the client
 * session).
 */
export type SourceMap = {
  /** The original markdown, normalized to "\n" line endings. */
  original: string;
  /** Per source id: byte range [start, end) in `original` and content hash. */
  blocks: { start: number; end: number; hash: string }[];
  /** Text before the first block and after the last block. */
  leading: string;
  trailing: string;
  style: Style;
  /** File had a UTF-8 byte order mark. */
  bom: boolean;
};
