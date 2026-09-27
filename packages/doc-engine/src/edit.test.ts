import { parse } from "./parse";
import { serialize } from "./serialize";
import type { BlockNode, DocNode, InlineNode } from "./schema";

const md = `---
title: First week
tags: [a, b]
---

# Welcome

Intro paragraph with *emphasis* and [a link](x.md).

* star list
* second item

1. one
2. two

| a | b |
|---|---|
| 1 | 2 |

~~~js
const x = 1;
~~~

{{< youtube id="abc" >}}
`;

function edit(doc: DocNode, i: number, f: (b: BlockNode) => BlockNode): DocNode {
  return { ...doc, content: doc.content.map((b, k) => (k === i ? f(b) : b)) };
}

test("model has the expected block types", () => {
  const { doc } = parse(md);
  expect(doc.content.map((b) => b.type)).toEqual(["frontmatter", "heading", "paragraph", "bulletList", "orderedList", "table", "codeBlock", "rawBlock"]);
});

test("editing a list re-serializes it in the file's style", () => {
  const { doc, sourceMap } = parse(md);
  const out = serialize(
    edit(doc, 3, (b) => {
      if (b.type !== "bulletList") return b;
      return { ...b, content: [...b.content, { type: "listItem", attrs: { checked: null, spread: false }, content: [{ type: "paragraph", content: [{ type: "text", text: "third" }] }] }] };
    }),
    sourceMap,
  );
  expect(out).toContain("* star list\n* second item\n* third");
  expect(out.replace("\n* third", "")).toBe(md);
});

test("editing frontmatter rewrites only the frontmatter", () => {
  const { doc, sourceMap } = parse(md);
  const out = serialize(edit(doc, 0, (b) => (b.type === "frontmatter" ? { ...b, attrs: { ...b.attrs, value: "title: Week one\ntags: [a, b]" } } : b)), sourceMap);
  expect(out).toBe(md.replace("title: First week", "title: Week one"));
});

test("inserting and deleting blocks keeps neighbours byte-exact", () => {
  const { doc, sourceMap } = parse(md);
  const inserted: DocNode = { ...doc, content: [...doc.content.slice(0, 3), { type: "paragraph", content: [{ type: "text", text: "New paragraph." }] }, ...doc.content.slice(3)] };
  const out = serialize(inserted, sourceMap);
  expect(out).toContain("[a link](x.md).\n\nNew paragraph.\n\n* star list");
  const deleted: DocNode = { ...doc, content: doc.content.filter((_, i) => i !== 5) };
  const out2 = serialize(deleted, sourceMap);
  expect(out2).not.toContain("| a | b |");
  expect(out2).toContain("2. two\n\n~~~js");
});

test("changed blocks use the file's fence and emphasis style", () => {
  const { doc, sourceMap } = parse(md);
  const out = serialize(
    edit(doc, 6, (b) => (b.type === "codeBlock" ? { ...b, content: [{ type: "text", text: "const y = 2;" }] } : b)),
    sourceMap,
  );
  expect(out).toContain("~~~js\nconst y = 2;\n~~~");
  const out2 = serialize(
    edit(doc, 2, (b) => (b.type === "paragraph" ? { ...b, content: [{ type: "text", text: "new ", marks: [] }, { type: "text", text: "stress", marks: [{ type: "italic" }] }] } : b)),
    sourceMap,
  );
  expect(out2).toContain("new *stress*");
});

test("CRLF files get CRLF in changed blocks", () => {
  const src = "# Title\r\n\r\nOne\r\ntwo\r\n";
  const { doc, sourceMap } = parse(src);
  expect(serialize(doc, sourceMap)).toBe(src);
  const out = serialize(edit(doc, 1, (b) => (b.type === "paragraph" ? { ...b, content: [{ type: "text", text: "Three\nfour" }] } : b)), sourceMap);
  expect(out).toBe("# Title\r\n\r\nThree\r\nfour\r\n");
});

test("serialize without a source map produces clean markdown", () => {
  const doc: DocNode = {
    type: "doc",
    content: [
      { type: "heading", attrs: { level: 2 }, content: [{ type: "text", text: "Hello" }] },
      { type: "paragraph", content: [{ type: "text", text: "Use " }, { type: "text", text: "kmdn", marks: [{ type: "bold" }, { type: "link", attrs: { href: "https://kmdn.app", title: null, ref: null } }] }] },
    ],
  };
  expect(serialize(doc)).toBe("## Hello\n\nUse [**kmdn**](https://kmdn.app)\n");
});

test("empty documents", () => {
  const { doc, sourceMap } = parse("");
  expect(doc.content).toEqual([]);
  expect(serialize(doc, sourceMap)).toBe("");
  expect(serialize({ type: "doc", content: [] })).toBe("");
});

describe("hoistWhitespace", () => {
  const b = [{ type: "bold" as const }];
  const md = (content: InlineNode[]) => serialize({ type: "doc", content: [{ type: "paragraph", content }] });
  test("moves edge spaces outside bold", () => {
    expect(md([{ type: "text", text: "a" }, { type: "text", text: " b ", marks: b }, { type: "text", text: "c" }])).toBe("a **b** c\n");
  });
  test("keeps inner spaces and whitespace-only runs plain", () => {
    expect(md([{ type: "text", text: "x " }, { type: "text", text: "  ", marks: b }, { type: "text", text: "y" }])).toBe("x   y\n");
    expect(md([{ type: "text", text: "p q", marks: b }, { type: "text", text: " ", marks: b }, { type: "text", text: "r" }])).toBe("**p q** r\n");
  });
  test("leaves links alone", () => {
    const link = [{ type: "link" as const, attrs: { href: "/x", title: null, ref: null } }];
    expect(md([{ type: "text", text: "see " }, { type: "text", text: "docs", marks: link }])).toBe("see [docs](/x)\n");
  });
});

test("adjacent lists stay separate lists", () => {
  const { doc, sourceMap } = parse("- a\n- b\n\nMiddle.\n\n- c\n- d\n");
  // Delete the paragraph between the lists.
  const without = { ...doc, content: doc.content.filter((b) => b.type !== "paragraph") };
  const out = serialize(without, sourceMap);
  expect(out).toBe("- a\n- b\n\n* c\n* d\n");
  expect(parse(out).doc.content.filter((b) => b.type === "bulletList")).toHaveLength(2);
  const ol = parse("1. a\n\nMiddle.\n\n1. b\n");
  const outOl = serialize({ ...ol.doc, content: ol.doc.content.filter((b) => b.type !== "paragraph") }, ol.sourceMap);
  expect(outOl).toBe("1. a\n\n1) b\n");
});
