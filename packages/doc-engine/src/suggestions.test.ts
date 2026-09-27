import { describe, expect, it } from "vitest";
import * as Y from "yjs";
import { canonical } from "./hash";
import { parse } from "./parse";
import { serialize } from "./serialize";
import type { DocNode } from "./schema";
import { listSuggestions, resolveSuggestions, withoutSuggestions } from "./suggestions";
import { CONTENT, applyDoc, readDoc, writeDoc } from "./ydoc";
import { nodeHash } from "./hash";

const ins = (id: string, author = "u1") => ({ type: "insertion", attrs: { id, author } });
const del = (id: string, author = "u1") => ({ type: "deletion", attrs: { id, author } });
const t = (text: string, ...marks: object[]) => (marks.length ? { type: "text", text, marks } : { type: "text", text });
const p = (content: object[], suggestion?: object) => ({ type: "paragraph", attrs: suggestion ? { suggestion } : {}, content });
const doc = (...content: object[]) => ({ type: "doc", content }) as unknown as DocNode;
const md = (d: DocNode) => serialize(d).trim();
const all = (d: DocNode, v: "accept" | "reject") => resolveSuggestions(d, () => v);

describe("suggestions", () => {
  it("excludes pending insertions and keeps pending deletions", () => {
    const d = doc(p([t("Hello "), t("big ", ins("s1")), t("old ", del("s2")), t("world")]));
    expect(md(d)).toBe("Hello old world");
    expect(md(all(d, "accept"))).toBe("Hello big world");
    expect(canonical(all(d, "accept"))).toBe(canonical(parse("Hello big world").doc).replace(/,"sid":\d+/g, "").replace(/"attrs":\{"sid":\d+\},?/g, ""));
  });

  it("merges text back into one node so hashes match a parsed page", () => {
    const d = all(doc(p([t("a"), t("b", del("x")), t("c")])), "reject");
    expect(d.content[0]).toEqual({ type: "paragraph", attrs: {}, content: [{ type: "text", text: "abc" }] });
  });

  it("resolves one suggestion at a time", () => {
    const d = doc(p([t("a"), t("B", ins("s1")), t("C", ins("s2"))]));
    const r = resolveSuggestions(d, (s) => (s.id === "s1" ? "accept" : null));
    expect(listSuggestions(r).map((s) => s.id)).toEqual(["s2"]);
    expect(md(r)).toBe("aB");
  });

  it("undoes a split: the inserted paragraph joins the previous one", () => {
    // "ab" split after "a", then "X" typed at the start of the new paragraph.
    const d = doc(p([t("a")]), p([t("X", ins("s1")), t("b")], { kind: "insert", id: "s1", author: "u1" }));
    expect(md(d)).toBe("ab");
    expect(md(all(d, "accept"))).toBe("a\n\nXb");
  });

  it("drops an inserted paragraph that only holds inserted text", () => {
    const d = doc(p([t("a")]), p([t("new", ins("s1"))], { kind: "insert", id: "s1", author: "u1" }), p([t("b")]));
    expect(md(d)).toBe("a\n\nb");
    expect(md(all(d, "accept"))).toBe("a\n\nnew\n\nb");
  });

  it("joins on an accepted cross-block deletion", () => {
    const d = doc(p([t("a"), t("b", del("s1"))]), p([t("c", del("s1")), t("d")], { kind: "join", id: "s1", author: "u1" }));
    expect(md(d)).toBe("ab\n\ncd");
    expect(md(all(d, "accept"))).toBe("ad");
  });

  it("joins split list items back", () => {
    const li = (content: object[], suggestion?: object) => ({ type: "listItem", attrs: { checked: null, spread: false, ...(suggestion ? { suggestion } : {}) }, content });
    const s = { kind: "insert", id: "s1", author: "u1" };
    const d = doc({ type: "bulletList", attrs: { spread: false }, content: [li([p([t("a")])]), li([p([t("b")], s)], s)] });
    expect(md(d)).toBe("- ab");
    expect(md(all(d, "accept"))).toBe("- a\n- b");
  });

  it("reverts a type change", () => {
    const d = doc({ type: "heading", attrs: { level: 2, suggestion: { kind: "change", id: "s1", author: "u1", from: { type: "paragraph", attrs: {} } } }, content: [t("Title")] });
    expect(md(d)).toBe("Title");
    expect(md(all(d, "accept"))).toBe("## Title");
    expect(listSuggestions(d)[0]).toMatchObject({ id: "s1", kinds: ["change"], change: { from: "paragraph", to: "heading" } });
  });

  it("removes a deleted block only once accepted", () => {
    const d = doc(p([t("a")]), { type: "horizontalRule", attrs: { suggestion: { kind: "delete", id: "s1", author: "u1" } } }, p([t("b")]));
    expect(md(d)).toBe("a\n\n---\n\nb");
    expect(md(all(d, "accept"))).toBe("a\n\nb");
  });

  it("lists suggestions with their text", () => {
    const d = doc(p([t("x", del("s1", "u2")), t("y", ins("s1", "u2")), t("z", ins("s2"))]));
    expect(listSuggestions(d)).toEqual([
      { id: "s1", author: "u2", at: undefined, inserted: "y", deleted: "x", kinds: [] },
      { id: "s2", author: "u1", at: undefined, inserted: "z", deleted: "", kinds: [] },
    ]);
  });

  it("leaves documents without suggestions alone", () => {
    const d = parse("# A\n\nb\n").doc;
    expect(withoutSuggestions(d)).toBe(d);
  });
});

describe("applying resolved suggestions to the Yjs document", () => {
  it("keeps the author's items when accepting (marks change in place)", () => {
    const y = new Y.Doc();
    y.clientID = 1;
    const frag = y.getXmlFragment(CONTENT);
    y.transact(() => writeDoc(frag, doc(p([t("keep "), t("added", ins("s1"))]), p([t("x")], { kind: "insert", id: "s1", author: "u1" }))));
    const server = new Y.Doc();
    server.clientID = 2;
    Y.applyUpdate(server, Y.encodeStateAsUpdate(y));
    const sfrag = server.getXmlFragment(CONTENT);
    server.transact(() => applyDoc(sfrag, all(readDoc(sfrag), "accept"), nodeHash));
    expect(md(readDoc(sfrag))).toBe("keep added\n\nx");
    expect(JSON.stringify(readDoc(sfrag))).not.toContain("insertion");
    // Nothing the server wrote is text: the content still belongs to client 1.
    let serverChars = 0;
    server.store.clients.get(2)?.forEach((s) => {
      if (s instanceof Y.Item && !s.deleted && s.countable && s.content.constructor.name === "ContentString") serverChars += s.length;
    });
    expect(serverChars).toBe(0);
  });
});
