import * as Y from "yjs";
import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { parse } from "./parse";
import { SourceSync } from "./source-sync";
import { CONTENT, applyDoc, readDoc, writeDoc } from "./ydoc";
import { SOURCE, serializeShared, writeSource } from "./shared-source";
import { nodeHash } from "./hash";
import { suggestEdit } from "./suggest-edit";
import { merge3 } from "./merge";

function replicas(markdown: string) {
  const parsed = parse(markdown);
  const seed = new Y.Doc();
  writeDoc(seed.getXmlFragment(CONTENT), parsed.doc);
  const make = () => {
    const doc = new Y.Doc();
    Y.applyUpdate(doc, Y.encodeStateAsUpdate(seed));
    return new SourceSync(doc, parsed.sourceMap);
  };
  return { a: make(), b: make(), sourceMap: parsed.sourceMap };
}

function exchange(a: SourceSync, b: SourceSync) {
  const ua = Y.encodeStateAsUpdate(a.doc);
  const ub = Y.encodeStateAsUpdate(b.doc);
  Y.applyUpdate(a.doc, ub);
  Y.applyUpdate(b.doc, ua);
}

test.each([
  ["# Title\n\n* item\n", "Title\n=====\n\n+ item\n"],
  ["# Title\n\nBody\n", "\ufeffTitle\r\n=====\r\n\r\n\r\nBody"],
  ["Hello\n", "\n\nHello\n\n\n"],
  ["", "\ufeff\r\n\r\n"],
])("source-only edits survive replica sync and reopening: %j", (original, edited) => {
  const { a, b, sourceMap } = replicas(original);
  const before = Y.encodeStateVector(a.doc);
  a.apply(edited);
  expect(Y.encodeStateVector(a.doc)).not.toEqual(before);
  exchange(a, b);
  expect(b.text()).toBe(edited);
  const reopened = new Y.Doc();
  Y.applyUpdate(reopened, Y.encodeStateAsUpdate(b.doc));
  expect(new SourceSync(reopened, sourceMap).text()).toBe(edited);
});

test("concurrent formatting of independent blocks survives on both replicas", () => {
  const original = "# First\n\nMiddle\n\n## Last\n";
  const { a, b } = replicas(original);
  a.apply(original.replace("# First", "First\n====="));
  b.apply(original.replace("## Last", "Last\n----"));
  exchange(a, b);
  const expected = "First\n=====\n\nMiddle\n\nLast\n----\n";
  expect(a.text()).toBe(expected);
  expect(b.text()).toBe(expected);
});

test("stale source never restores a concurrently deleted block or removes an inserted block", () => {
  const { a, b } = replicas("# First\n\nDelete me\n\n# Last\n");
  a.apply("First\n=====\n\nDelete me\n\n# Last\n");
  b.apply("# New first\n\n# First\n\n# Last\n");
  exchange(a, b);
  expect(a.text()).toBe("# New first\n\nFirst\n=====\n\n# Last\n");
  expect(b.text()).toBe(a.text());
});

test("duplicate blocks retain separate source ownership", () => {
  const original = "**Same**\n\n**Same**\n\nTail\n";
  const { a, b } = replicas(original);
  a.apply("__Same__\n\n**Same**\n\nTail\n");
  b.apply("**Same**\n\n**Same**\n\nTail changed\n");
  exchange(a, b);
  expect(b.text()).toBe("__Same__\n\n**Same**\n\nTail changed\n");
});

function authored(markdown: string) {
  const doc = new Y.Doc();
  const parsed = parse(markdown);
  doc.transact(() => {
    writeDoc(doc.getXmlFragment(CONTENT), parsed.doc);
    writeSource(doc, parsed);
  });
  return doc;
}

test("a deleted block's source cannot move onto an identical surviving block", () => {
  const { a, b } = replicas("**Same**\n\n**Same**\n");
  a.apply("__Same__\n\n**Same**\n");
  b.doc.getXmlFragment(CONTENT).delete(0, 1);
  exchange(a, b);
  expect(b.text()).toBe("**Same**\n");
});

test("already shared texts merge independent edits with no document-wide overwrite", () => {
  const seed = authored("# First\n\n## Last\n");
  const a = new SourceSync(seed);
  const replica = new Y.Doc();
  Y.applyUpdate(replica, Y.encodeStateAsUpdate(seed));
  const b = new SourceSync(replica);
  a.apply("First\n=====\n\n## Last\n");
  b.apply("# First\n\nLast\n----\n");
  exchange(a, b);
  expect(a.text()).toBe("First\n=====\n\nLast\n----\n");
  expect(b.text()).toBe(a.text());
});

test("untrusted source text cannot replace live semantics or add blocks", () => {
  const doc = authored("# Real\n\nKeep me\n");
  const record = [...doc.getMap(SOURCE).values()].find((v) => v instanceof Y.Map) as Y.Map<unknown>;
  const body = record.get("body") as Y.Text;
  body.delete(0, body.length);
  body.insert(0, "# Forged\n\nExtra injected paragraph");
  const output = serializeShared(doc);
  expect(output).not.toContain("Forged");
  expect(parse(output).doc.content.map(nodeHash)).toEqual(parse("# Real\n\nKeep me\n").doc.content.map(nodeHash));
});

test.each(["nested", "key", "next", "embed", "attributes"])("rejects malformed or unbounded %s metadata", (kind) => {
  const doc = authored("Small\n");
  const source = doc.getMap<unknown>(SOURCE);
  const record = [...source.values()].find((v) => v instanceof Y.Map) as Y.Map<unknown>;
  if (kind === "nested") record.set("unknown", new Y.Map([["payload", "x".repeat(1000)]]));
  if (kind === "key") source.set("x".repeat(1000), new Y.Text(""));
  if (kind === "next") record.set("next", "x".repeat(1000));
  if (kind === "embed") (record.get("body") as Y.Text).insertEmbed(0, { payload: "x".repeat(1000) });
  if (kind === "attributes") (record.get("body") as Y.Text).format(0, 1, { payload: "x".repeat(1000) });
  expect(() => serializeShared(doc, undefined, 64)).toThrow(/authored source/);
});

test("bounds aggregate UTF-8 source bytes before parsing metadata", () => {
  const doc = authored("漢字漢字漢字\n");
  expect(() => serializeShared(doc, undefined, 15)).toThrow("input limit");
  const many = authored("First\n\nSecond\n\nThird\n");
  expect(() => serializeShared(many, undefined, 16)).toThrow("input limit");
});

test("shared source patches never split UTF-16 surrogate pairs", () => {
  const seed = authored("# Emoji 😀\n");
  const a = new SourceSync(seed);
  const second = new Y.Doc();
  Y.applyUpdate(second, Y.encodeStateAsUpdate(seed));
  const b = new SourceSync(second);
  a.apply("Emoji 😁\n=======\n");
  exchange(a, b);
  expect(b.text()).toBe("Emoji 😁\n=======\n");
});

const fixtures = join(import.meta.dirname, "../../../testdata/fidelity");
test.each(readdirSync(fixtures))("shared source preserves the fidelity fixture %s", (file) => {
  const text = readFileSync(join(fixtures, file), "utf8");
  const seed = authored(text);
  const replica = new Y.Doc();
  Y.applyUpdate(replica, Y.encodeStateAsUpdate(seed));
  expect(serializeShared(replica)).toBe(text);
});

test("pending suggestions do not replace authored bytes in unchanged blocks", () => {
  const text = "Title\n=====\n\nKeep   *this*\n";
  const doc = authored(text);
  const fragment = doc.getXmlFragment(CONTENT);
  const target = suggestEdit(readDoc(fragment), parse("# Title\n\nKeep   *this* and more\n").doc, { id: "suggestion", author: "a" });
  doc.transact(() => applyDoc(fragment, target, nodeHash));
  expect(serializeShared(doc)).toBe(text);
});

test("unresolved conflicts keep the revision side and untouched authored blocks", () => {
  const text = "Title\n=====\n\nOurs\n";
  const doc = authored(text);
  const merged = merge3("# Title\n\nBase\n", text, "# Title\n\nTheirs\n");
  expect(merged.conflicts).toBe(1);
  doc.transact(() => applyDoc(doc.getXmlFragment(CONTENT), merged.doc, nodeHash));
  expect(serializeShared(doc)).toBe(text);
});
