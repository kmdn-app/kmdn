/**
 * The editor schema: Tiptap extensions shaped exactly like the doc-engine
 * model (packages/doc-engine/src/schema.ts). Node and mark names, attributes
 * and content expressions must accept everything the parser produces: when
 * y-prosemirror meets a node the schema rejects, it deletes it from the shared
 * document. schema.test.ts checks this against the fidelity fixtures.
 */
import { Extension, Mark, Node, mergeAttributes, textblockTypeInputRule, type AnyExtension } from "@tiptap/core";
import { userColor } from "@/components/avatar";
import Document from "@tiptap/extension-document";
import Text from "@tiptap/extension-text";
import Paragraph from "@tiptap/extension-paragraph";
import Heading from "@tiptap/extension-heading";
import Blockquote from "@tiptap/extension-blockquote";
import { BulletList, ListItem, OrderedList, ListKeymap } from "@tiptap/extension-list";
import CodeBlock from "@tiptap/extension-code-block";
import HorizontalRule from "@tiptap/extension-horizontal-rule";
import HardBreak from "@tiptap/extension-hard-break";
import Bold from "@tiptap/extension-bold";
import Italic from "@tiptap/extension-italic";
import Strike from "@tiptap/extension-strike";
import Code from "@tiptap/extension-code";
import Link from "@tiptap/extension-link";

/** Top-level block types; they carry the source id (`sid`) linking them to their original bytes. */
export const TOP_BLOCKS = [
  "paragraph",
  "heading",
  "blockquote",
  "bulletList",
  "orderedList",
  "codeBlock",
  "mathBlock",
  "table",
  "horizontalRule",
  "footnoteDefinition",
  "frontmatter",
  "rawBlock",
];

const hidden = <T>(def: T) => ({ default: def, rendered: false });

/**
 * `sid` never splits: when a paragraph is split, the new half gets none, so
 * the serializer can't mistake it for the original block.
 */
const SourceIds = Extension.create({
  name: "sourceIds",
  addGlobalAttributes() {
    return [{ types: TOP_BLOCKS, attributes: { sid: { default: null, rendered: false, keepOnSplit: false } } }];
  },
});

/** Every node type that can carry a node-level suggestion (see suggest.ts). */
export const SUGGESTABLE = [...TOP_BLOCKS, "listItem", "tableRow", "tableHeader", "tableCell"];

/**
 * Node-level suggestions `{kind, id, author, at, from?}`. Never kept on split
 * (the new half is marked by the suggestion itself) and never parsed from
 * pasted HTML.
 */
const Suggestions = Extension.create({
  name: "suggestionAttrs",
  addGlobalAttributes() {
    return [
      {
        types: SUGGESTABLE,
        attributes: {
          suggestion: {
            default: null,
            keepOnSplit: false,
            parseHTML: () => null,
            renderHTML: (a: { suggestion?: { kind: string; author: string } | null }) =>
              a.suggestion ? { "data-suggestion": a.suggestion.kind, style: `--author: ${userColor(a.suggestion.author)}` } : {},
          },
        },
      },
    ];
  },
});

const suggestionMark = (name: string, tag: string) =>
  Mark.create({
    name,
    inclusive: false,
    // Rank after formatting marks, like the doc engine's MARK_ORDER.
    priority: 90,
    addAttributes: () => ({ id: { default: "" }, author: { default: "" }, at: { default: null } }),
    parseHTML: () => [],
    renderHTML: ({ HTMLAttributes }) => [
      tag,
      { class: `suggest-${name}`, "data-suggestion-id": HTMLAttributes.id, style: `--author: ${userColor(String(HTMLAttributes.author ?? ""))}` },
      0,
    ],
  });

export const Insertion = suggestionMark("insertion", "ins");
export const Deletion = suggestionMark("deletion", "del");

const Doc = Document.extend({ content: "block*" });

const Heading_ = Heading.extend({
  addAttributes() {
    return { level: hidden(1) };
  },
});

const Blockquote_ = Blockquote.extend({ content: "block*" });

const BulletList_ = BulletList.extend({
  addAttributes() {
    return { spread: hidden(false) };
  },
});

const OrderedList_ = OrderedList.extend({
  addAttributes() {
    return {
      start: {
        default: 1,
        parseHTML: (el: HTMLElement) => (el.hasAttribute("start") ? parseInt(el.getAttribute("start") ?? "1", 10) : 1),
        renderHTML: (a: { start: number }) => (a.start !== 1 ? { start: a.start } : {}),
      },
      spread: hidden(false),
    };
  },
});

/** List items hold any blocks; `checked` makes them task items (GFM). */
const ListItem_ = ListItem.extend({
  content: "block*",
  addAttributes() {
    return {
      checked: {
        default: null,
        parseHTML: (el: HTMLElement) => (el.hasAttribute("data-checked") ? el.getAttribute("data-checked") === "true" : null),
        renderHTML: (a: { checked: boolean | null }) => (a.checked === null ? {} : { "data-checked": String(a.checked), "data-task": "" }),
      },
      spread: hidden(false),
    };
  },
  addNodeView() {
    return ({ node, getPos, editor }) => {
      const li = document.createElement("li");
      const content = document.createElement("div");
      content.className = "li-content";
      let box: HTMLInputElement | null = null;
      const render = (checked: boolean | null) => {
        li.toggleAttribute("data-task", checked !== null);
        if (checked === null) {
          box?.remove();
          box = null;
          return;
        }
        if (!box) {
          box = document.createElement("input");
          box.type = "checkbox";
          box.contentEditable = "false";
          box.addEventListener("mousedown", (e) => e.preventDefault());
          box.addEventListener("click", () => {
            const pos = typeof getPos === "function" ? getPos() : undefined;
            if (pos === undefined || !editor.isEditable) return;
            const cur = editor.state.doc.nodeAt(pos);
            editor.view.dispatch(editor.state.tr.setNodeAttribute(pos, "checked", !cur?.attrs.checked));
          });
          li.prepend(box);
        }
        box.checked = checked;
        box.disabled = !editor.isEditable;
      };
      const suggested = (sg: { kind: string; author: string } | null) => {
        if (sg) {
          li.dataset.suggestion = sg.kind;
          li.style.setProperty("--author", userColor(sg.author));
        } else {
          delete li.dataset.suggestion;
          li.style.removeProperty("--author");
        }
      };
      li.append(content);
      render(node.attrs.checked as boolean | null);
      suggested(node.attrs.suggestion);
      return {
        dom: li,
        contentDOM: content,
        update: (n) => {
          if (n.type.name !== "listItem") return false;
          render(n.attrs.checked as boolean | null);
          suggested(n.attrs.suggestion);
          return true;
        },
        ignoreMutation: (m) => m.target === box || (m.type === "attributes" && m.target === li),
      };
    };
  },
});

const CodeBlock_ = CodeBlock.extend({
  marks: "insertion deletion",
  addAttributes() {
    return {
      lang: {
        default: null,
        parseHTML: (el: HTMLElement) =>
          [...(el.firstElementChild?.classList ?? [])].find((c) => c.startsWith("language-"))?.slice("language-".length) ?? null,
        rendered: false,
      },
      meta: hidden(null),
    };
  },
  renderHTML({ node, HTMLAttributes }) {
    return ["pre", mergeAttributes(HTMLAttributes, { "data-lang": node.attrs.lang ?? "" }), ["code", { class: node.attrs.lang ? `language-${node.attrs.lang}` : null }, 0]];
  },
  addInputRules() {
    return [
      textblockTypeInputRule({ find: /^```([a-z0-9+#-]+)?[\s\n]$/i, type: this.type, getAttributes: (m) => ({ lang: m[1] ?? null }) }),
      textblockTypeInputRule({ find: /^~~~([a-z0-9+#-]+)?[\s\n]$/i, type: this.type, getAttributes: (m) => ({ lang: m[1] ?? null }) }),
    ];
  },
});

const Code_ = Code.extend({ excludes: "" });

const Link_ = Link.extend({
  inclusive: false,
  addAttributes() {
    return {
      href: { default: null },
      title: { default: null },
      ref: hidden(null),
    };
  },
}).configure({ openOnClick: false, autolink: false, linkOnPaste: true });

// --- Nodes the doc engine has that Tiptap doesn't ship ------------------------

export type ImageOptions = { resolve: (src: string) => string };

export const Image = Node.create<ImageOptions>({
  name: "image",
  group: "inline",
  inline: true,
  atom: true,
  draggable: true,
  addOptions: () => ({ resolve: (s: string) => s }),
  addAttributes: () => ({ src: { default: "" }, alt: { default: "" }, title: { default: null }, ref: hidden(null) }),
  parseHTML: () => [{ tag: "img[src]" }],
  renderHTML({ HTMLAttributes }) {
    return ["img", mergeAttributes(HTMLAttributes, { src: this.options.resolve(String(HTMLAttributes.src ?? "")) })];
  },
});

const inlineAtom = (name: string, attrs: Record<string, unknown>, text: (a: Record<string, unknown>) => string, cls: string) =>
  Node.create({
    name,
    group: "inline",
    inline: true,
    atom: true,
    addAttributes: () => Object.fromEntries(Object.entries(attrs).map(([k, v]) => [k, { default: v }])),
    parseHTML: () => [{ tag: `span[data-kmdn="${name}"]` }],
    renderHTML: ({ node }) => ["span", { "data-kmdn": name, class: cls }, text(node.attrs)],
  });

export const MathInline = inlineAtom("mathInline", { source: "" }, (a) => `$${String(a.source)}$`, "math-inline");
export const FootnoteReference = inlineAtom("footnoteReference", { label: "" }, (a) => `[^${String(a.label)}]`, "fn-ref");
export const RawInline = inlineAtom("rawInline", { raw: "" }, (a) => String(a.raw), "raw-inline");

export const MathBlock = Node.create({
  name: "mathBlock",
  group: "block",
  content: "text*",
  marks: "insertion deletion",
  code: true,
  defining: true,
  parseHTML: () => [{ tag: 'pre[data-kmdn="math"]', preserveWhitespace: "full" }],
  renderHTML: () => ["pre", { "data-kmdn": "math", class: "math-block" }, ["code", 0]],
});

export const FootnoteDefinition = Node.create({
  name: "footnoteDefinition",
  group: "block",
  content: "block*",
  defining: true,
  addAttributes: () => ({ label: { default: "" } }),
  parseHTML: () => [{ tag: 'div[data-kmdn="footnote"]' }],
  renderHTML: ({ node }) => ["div", { "data-kmdn": "footnote", class: "fn-def", "data-label": node.attrs.label }, 0],
});

export const Frontmatter = Node.create({
  name: "frontmatter",
  group: "block",
  atom: true,
  selectable: true,
  addAttributes: () => ({ format: { default: "yaml" }, value: { default: "" } }),
  parseHTML: () => [{ tag: 'div[data-kmdn="frontmatter"]' }],
  renderHTML: ({ node }) => ["div", { "data-kmdn": "frontmatter", class: "frontmatter" }, ["pre", String(node.attrs.value)]],
});

export const RawBlock = Node.create({
  name: "rawBlock",
  group: "block",
  atom: true,
  selectable: true,
  addAttributes: () => ({ raw: { default: "" }, kind: { default: "unknown" } }),
  parseHTML: () => [{ tag: 'div[data-kmdn="raw"]' }],
  renderHTML: ({ node }) => ["div", { "data-kmdn": "raw", "data-kind": node.attrs.kind, class: "rawblk" }, ["pre", String(node.attrs.raw)]],
});

export const Table = Node.create({
  name: "table",
  group: "block",
  content: "tableRow+",
  isolating: true,
  addAttributes: () => ({ align: hidden([] as (string | null)[]) }),
  parseHTML: () => [{ tag: "table" }],
  renderHTML: () => ["div", { class: "table-wrap" }, ["table", ["tbody", 0]]],
});

export const TableRow = Node.create({
  name: "tableRow",
  content: "(tableHeader | tableCell)*",
  parseHTML: () => [{ tag: "tr" }],
  renderHTML: () => ["tr", 0],
});

const cell = (name: string, tag: string) =>
  Node.create({
    name,
    content: "inline*",
    isolating: true,
    parseHTML: () => [{ tag }],
    renderHTML: () => [tag, 0],
  });

export const TableHeader = cell("tableHeader", "th");
export const TableCell = cell("tableCell", "td");

// --- Conflicts from updates from Published (docs/specs/06-git-and-forges.md) --

/** An unresolved merge: the Published side, then the revision's. */
export const Conflict = Node.create({
  name: "conflict",
  group: "block",
  content: "conflictSide conflictSide",
  isolating: true,
  defining: true,
  addAttributes: () => ({ id: { default: "" } }),
  parseHTML: () => [{ tag: 'div[data-kmdn="conflict"]' }],
  renderHTML: () => ["div", { "data-kmdn": "conflict", class: "conflict" }, 0],
});

export type ConflictLabels = { published: string; revision: string };

export const ConflictSide = Node.create<{ labels: ConflictLabels }>({
  name: "conflictSide",
  content: "block*",
  isolating: true,
  addOptions: () => ({ labels: { published: "Published", revision: "This revision" } }),
  addAttributes: () => ({ side: { default: "revision" } }),
  parseHTML: () => [{ tag: "div[data-side]" }],
  renderHTML({ node }) {
    const side = node.attrs.side as keyof ConflictLabels;
    return ["div", { class: "conflict-side", "data-side": side, "data-label": this.options.labels[side] ?? side }, 0];
  },
});

/** Everything the page schema needs, without collaboration or UI helpers. */
export function schemaExtensions(opts: { resolveImage?: (src: string) => string; conflictLabels?: ConflictLabels } = {}): AnyExtension[] {
  return [
    Doc,
    Text,
    Paragraph.extend({ addAttributes: () => ({}) }),
    Heading_.configure({ levels: [1, 2, 3, 4, 5, 6] }),
    Blockquote_,
    BulletList_,
    OrderedList_,
    ListItem_,
    ListKeymap,
    CodeBlock_,
    MathBlock,
    Table,
    TableRow,
    TableHeader,
    TableCell,
    HorizontalRule,
    FootnoteDefinition,
    Frontmatter,
    RawBlock,
    Conflict,
    opts.conflictLabels ? ConflictSide.configure({ labels: opts.conflictLabels }) : ConflictSide,
    HardBreak,
    Image.configure({ resolve: opts.resolveImage ?? ((s: string) => s) }),
    MathInline,
    FootnoteReference,
    RawInline,
    Link_,
    Bold,
    Italic,
    Strike,
    Code_,
    Insertion,
    Deletion,
    SourceIds,
    Suggestions,
  ];
}

