/**
 * The collaborative encoding must not lose anything: a page loaded into a
 * Y.Doc, synced to another replica and read back serializes to the same bytes.
 */
import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import * as Y from "yjs";
import { parse } from "./parse";
import { serialize } from "./serialize";
import { canonical } from "./hash";
import { CONTENT, applyDoc, readDoc, writeDoc } from "./ydoc";
import { nodeHash } from "./hash";

const fixtures = join(import.meta.dirname, "../../../testdata/fidelity");

function viaY(src: string) {
  const { doc, sourceMap } = parse(src);
  const a = new Y.Doc();
  writeDoc(a.getXmlFragment(CONTENT), doc);
  const b = new Y.Doc();
  Y.applyUpdate(b, Y.encodeStateAsUpdate(a));
  return { doc, sourceMap, back: readDoc(b.getXmlFragment(CONTENT)) };
}

describe.each(readdirSync(fixtures).map((f) => [f]))("%s", (f) => {
  const src = readFileSync(join(fixtures, f), "utf8");
  test("survives a Y.Doc round trip", () => {
    const { doc, sourceMap, back } = viaY(src);
    expect(canonical(back)).toBe(canonical(doc));
    expect(serialize(back, sourceMap)).toBe(src);
  });
});

test("edits made through Yjs serialize with the rest of the file untouched", () => {
  const src = "# Title\n\nFirst   paragraph  kept *as is*.\n\n* one\n* two\n\nSecond paragraph.\n";
  const { sourceMap } = parse(src);
  const { doc } = parse(src);
  const y = new Y.Doc();
  const frag = y.getXmlFragment(CONTENT);
  writeDoc(frag, doc);
  // Append " Edited." to the last paragraph, as the editor would.
  const last = frag.get(frag.length - 1) as Y.XmlElement;
  const text = last.get(0) as Y.XmlText;
  text.insert(text.length, " Edited.", { bold: {} });
  const out = serialize(readDoc(frag), sourceMap);
  expect(out).toBe("# Title\n\nFirst   paragraph  kept *as is*.\n\n* one\n* two\n\nSecond paragraph. **Edited.**\n");
});

test("marks map to y-prosemirror formatting attributes", () => {
  const { doc } = parse("A [link](https://x.dev \"T\") and **bold `code`**.\n");
  const y = new Y.Doc();
  writeDoc(y.getXmlFragment(CONTENT), doc);
  const p = y.getXmlFragment(CONTENT).get(0) as Y.XmlElement;
  expect(p.nodeName).toBe("paragraph");
  const delta = (p.get(0) as Y.XmlText).toDelta();
  expect(delta[1]).toEqual({ insert: "link", attributes: { link: { href: "https://x.dev", title: "T", ref: null } } });
  expect(delta[4]).toEqual({ insert: "code", attributes: { bold: {}, code: {} } });
});

describe("applyDoc", () => {
  const setup = (src: string) => {
    const { doc, sourceMap } = parse(src);
    const y = new Y.Doc();
    writeDoc(y.getXmlFragment(CONTENT), doc);
    return { y, sourceMap };
  };
  const apply = (y: Y.Doc, md: string) => {
    const sv = Y.encodeStateVector(y);
    y.transact(() => applyDoc(y.getXmlFragment(CONTENT), parse(md).doc, nodeHash));
    return Y.encodeStateAsUpdate(y, sv);
  };

  test("no change produces an empty update", () => {
    const src = "# A\n\nText.\n";
    const { y } = setup(src);
    expect(apply(y, src).length).toBeLessThanOrEqual(2);
  });

  test("inserts, deletes and edits blocks, keeping untouched bytes", () => {
    const src = "# Title\n\nKeep   this *spacing*.\n\n* a\n* b\n\nChange me.\n\nDrop me.\n";
    const { y, sourceMap } = setup(src);
    apply(y, "# Title\n\nKeep   this *spacing*.\n\n* a\n* b\n\nChanged **you**.\n\nNew paragraph.\n");
    expect(serialize(readDoc(y.getXmlFragment(CONTENT)), sourceMap)).toBe("# Title\n\nKeep   this *spacing*.\n\n* a\n* b\n\nChanged **you**.\n\nNew paragraph.\n");
  });

  test("patches a paragraph in place so a concurrent edit to it survives", () => {
    const src = "Alpha beta gamma.\n";
    const a = setup(src).y;
    const b = new Y.Doc();
    Y.applyUpdate(b, Y.encodeStateAsUpdate(a));
    // Replica B types at the start while the server rewrites the end.
    const tb = (b.getXmlFragment(CONTENT).get(0) as Y.XmlElement).get(0) as Y.XmlText;
    tb.insert(0, "Hey. ");
    const fromServer = apply(a, "Alpha beta delta.\n");
    Y.applyUpdate(b, fromServer);
    Y.applyUpdate(a, Y.encodeStateAsUpdate(b));
    const out = serialize(readDoc(a.getXmlFragment(CONTENT)));
    expect(out).toBe("Hey. Alpha beta delta.\n");
    expect(serialize(readDoc(b.getXmlFragment(CONTENT)))).toBe(out);
  });

  test("mark changes inside a paragraph", () => {
    const { y } = setup("Make this bold.\n");
    apply(y, "Make **this** bold.\n");
    expect(serialize(readDoc(y.getXmlFragment(CONTENT)))).toBe("Make **this** bold.\n");
    apply(y, "Make this bold.\n");
    expect(serialize(readDoc(y.getXmlFragment(CONTENT)))).toBe("Make this bold.\n");
  });

  test("type changes replace the block", () => {
    const { y } = setup("Heading soon\n\nBody.\n");
    apply(y, "## Heading soon\n\nBody.\n");
    expect(serialize(readDoc(y.getXmlFragment(CONTENT)))).toBe("## Heading soon\n\nBody.\n");
  });
});
