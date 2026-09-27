import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { getSchema } from "@tiptap/core";
import { Node as PMNode } from "@tiptap/pm/model";
import * as Y from "yjs";
import { prosemirrorJSONToYXmlFragment, yXmlFragmentToProseMirrorRootNode } from "@tiptap/y-tiptap";
import { CONTENT, canonical, parse, readDoc, serialize, sortMarks, writeDoc, type Mark } from "@kmdn/doc-engine";
import { schemaExtensions } from "./schema";

const schema = getSchema(schemaExtensions());
const fixtures = join(import.meta.dirname, "../../../../testdata/fidelity");

type J = { type: string; attrs?: Record<string, unknown>; marks?: Mark[]; content?: J[]; text?: string };

/** ProseMirror orders marks by schema rank; the doc engine by MARK_ORDER. */
function norm(n: J): J {
  return {
    ...n,
    ...(n.marks ? { marks: sortMarks(n.marks) } : {}),
    ...(n.content ? { content: n.content.map(norm) } : {}),
  };
}

describe.each(readdirSync(fixtures).map((f) => [f]))("%s", (f) => {
  const src = readFileSync(join(fixtures, f), "utf8");
  const { doc, sourceMap } = parse(src);

  test("fits the editor schema", () => {
    const pm = PMNode.fromJSON(schema, doc);
    pm.check();
    expect(canonical(norm(pm.toJSON() as J))).toBe(canonical(norm(doc as J)));
  });

  test("loads through y-prosemirror from a server-made document", () => {
    const y = new Y.Doc();
    writeDoc(y.getXmlFragment(CONTENT), doc);
    const root = yXmlFragmentToProseMirrorRootNode(y.getXmlFragment(CONTENT), schema);
    root.check();
    expect(canonical(norm(root.toJSON() as J))).toBe(canonical(norm(doc as J)));
  });

  test("an editor-written document serializes to the same bytes", () => {
    const y = new Y.Doc();
    prosemirrorJSONToYXmlFragment(schema, PMNode.fromJSON(schema, doc).toJSON(), y.getXmlFragment(CONTENT));
    expect(serialize(readDoc(y.getXmlFragment(CONTENT)), sourceMap)).toBe(src);
  });
});
