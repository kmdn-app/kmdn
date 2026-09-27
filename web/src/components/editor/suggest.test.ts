import { getSchema } from "@tiptap/core";
import { Node as PMNode } from "@tiptap/pm/model";
import { EditorState, TextSelection, type Transaction } from "@tiptap/pm/state";
import * as Y from "yjs";
import { initProseMirrorDoc, updateYFragment, yXmlFragmentToProseMirrorRootNode } from "@tiptap/y-tiptap";
import { CONTENT, applyDoc, canonical, listSuggestions, nodeHash, parse, readDoc, resolveSuggestions, serialize, sortMarks, writeDoc, type DocNode, type Mark } from "@kmdn/doc-engine";
import { schemaExtensions } from "./schema";
import { editorSuggestions, suggestTransaction, type SuggestContext } from "./suggest";

const schema = getSchema(schemaExtensions());

function stateOf(md: string): EditorState {
  return EditorState.create({ schema, doc: PMNode.fromJSON(schema, parse(md).doc) });
}

let n = 0;
const ctx = (author = "u1"): SuggestContext => ({ author, now: 1, newId: () => `s${++n}` });

/** Applies a transaction in suggesting mode (null: refused). */
function suggest(state: EditorState, build: (tr: Transaction) => void, author = "u1"): EditorState | null {
  const tr = state.tr;
  build(tr);
  const out = suggestTransaction(state, tr, ctx(author));
  if (!out) return null;
  const next = state.apply(out);
  next.doc.check();
  return next;
}

const md = (s: EditorState, d: "accept" | "reject") => serialize(resolveSuggestions(s.doc.toJSON() as DocNode, () => d)).trim();
/** Position right after the first occurrence of text. */
function after(s: EditorState, text: string): number {
  let at = -1;
  s.doc.descendants((node, pos) => {
    if (at < 0 && node.isText && node.text!.includes(text)) at = pos + node.text!.indexOf(text) + text.length;
  });
  if (at < 0) throw new Error(`no ${text}`);
  return at;
}

describe("suggesting", () => {
  it("records typing as one insertion", () => {
    let s = stateOf("Hello world\n");
    const at = after(s, "Hello");
    s = suggest(s, (tr) => tr.insertText(",", at))!;
    s = suggest(s, (tr) => tr.insertText(" dear", at + 1))!;
    expect(md(s, "reject")).toBe("Hello world");
    expect(md(s, "accept")).toBe("Hello, dear world");
    expect(editorSuggestions(s.doc)).toMatchObject([{ inserted: ", dear", deleted: "" }]);
  });

  it("keeps deleted text, marked, and moves the cursor past it", () => {
    let s = stateOf("Hello world\n");
    const end = after(s, "world");
    s = s.apply(s.tr.setSelection(TextSelection.create(s.doc, end)));
    for (let i = 0; i < 3; i++) {
      const h = s.selection.head;
      s = suggest(s, (tr) => tr.delete(h - 1, h))!;
    }
    expect(s.selection.head).toBe(end - 3);
    expect(md(s, "reject")).toBe("Hello world");
    expect(md(s, "accept")).toBe("Hello wo");
    expect(editorSuggestions(s.doc)).toMatchObject([{ deleted: "rld" }]);
  });

  it("removes your own insertion for real, but not someone else's", () => {
    let s = stateOf("ab\n");
    const at = after(s, "a");
    s = suggest(s, (tr) => tr.insertText("XY", at))!;
    s = suggest(s, (tr) => tr.delete(at, at + 2))!;
    expect(editorSuggestions(s.doc)).toEqual([]);
    s = suggest(s, (tr) => tr.insertText("XY", at))!;
    s = suggest(s, (tr) => tr.delete(at, at + 2), "u2")!;
    expect(md(s, "reject")).toBe("ab");
    expect(editorSuggestions(s.doc).map((x) => x.author)).toEqual(["u1", "u2"]);
  });

  it("records a replacement as one suggestion", () => {
    let s = stateOf("the cat sat\n");
    const to = after(s, "cat");
    s = suggest(s, (tr) => tr.insertText("dog", to - 3, to))!;
    expect(md(s, "reject")).toBe("the cat sat");
    expect(md(s, "accept")).toBe("the dog sat");
    expect(editorSuggestions(s.doc)).toMatchObject([{ inserted: "dog", deleted: "cat" }]);
  });

  it("records splits and joins", () => {
    let s = stateOf("onetwo\n\nthree\n");
    s = suggest(s, (tr) => tr.split(after(s, "one")))!;
    expect(md(s, "reject")).toBe("onetwo\n\nthree");
    expect(md(s, "accept")).toBe("one\n\ntwo\n\nthree");
    // Typing in the new paragraph is part of the same suggestion.
    const start = after(s, "one") + 2;
    s = suggest(s, (tr) => tr.insertText("New ", start))!;
    expect(editorSuggestions(s.doc)).toHaveLength(1);
    let j = stateOf("one\n\ntwo\n");
    const end = after(j, "one");
    j = suggest(j, (tr) => tr.delete(end, end + 2))!;
    expect(md(j, "reject")).toBe("one\n\ntwo");
    expect(md(j, "accept")).toBe("onetwo");
  });

  it("records a type change", () => {
    let s = stateOf("Title\n\nbody\n");
    s = suggest(s, (tr) => tr.setBlockType(1, 1, schema.nodes.heading!, { level: 2 }))!;
    expect(md(s, "reject")).toBe("Title\n\nbody");
    expect(md(s, "accept")).toBe("## Title\n\nbody");
    // Changing it back leaves nothing to review.
    s = suggest(s, (tr) => tr.setBlockType(1, 1, schema.nodes.paragraph!))!;
    expect(editorSuggestions(s.doc)).toEqual([]);
  });

  it("refuses what it can't record yet", () => {
    const s = stateOf("a\n");
    expect(suggest(s, (tr) => tr.addMark(1, 2, schema.marks.bold!.create()))).toBeNull();
  });
});

// Spike S4: random edits in suggesting mode never change what the page
// publishes until accepted, keep the document valid, and accepting a single
// edit gives what editing directly would have.
describe("suggesting (fuzz)", () => {
  const SAMPLE = "# Title\n\nFirst paragraph with some words.\n\nSecond one here.\n\n- item one\n- item two\n\n## Sub\n\nLast line.\n";

  function rng(seed: number) {
    return () => {
      seed = (seed + 0x6d2b79f5) | 0;
      let t = Math.imul(seed ^ (seed >>> 15), 1 | seed);
      t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
      return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
    };
  }

  function textPositions(doc: PMNode): number[] {
    const out: number[] = [];
    doc.descendants((node, pos) => {
      if (node.inlineContent) for (let i = 0; i <= node.content.size; i++) out.push(pos + 1 + i);
      return !node.inlineContent;
    });
    return out;
  }

  type Op = ((tr: Transaction) => void) & { desc?: string };
  function randomOp(doc: PMNode, r: () => number): Op {
    const ps = textPositions(doc);
    const p = ps[Math.floor(r() * ps.length)]!;
    const q = ps[Math.min(ps.length - 1, ps.indexOf(p) + 1 + Math.floor(r() * 12))]!;
    const k = Math.floor(r() * 8);
    const parent = doc.resolve(p).parent.type.name;
    const f = pick(k, p, q, parent, r);
    f.desc = `${k}:${p}-${q}`;
    return f;
  }
  function pick(k: number, p: number, q: number, parent: string, r: () => number): Op {
    switch (k) {
      case 0:
        return (tr) => tr.insertText("xy", p);
      case 1:
        return (tr) => tr.delete(p, q);
      case 2:
        return (tr) => tr.insertText("Z", p, q);
      case 3:
        return (tr) => (parent === "paragraph" || parent === "heading" ? tr.split(p) : tr.insertText("s", p));
      case 4: {
        const level = 1 + Math.floor(r() * 3);
        return (tr) => tr.setBlockType(p, p, schema.nodes.heading!, { level });
      }
      case 5:
        return (tr) => tr.delete(Math.max(1, p - 1), p);
      case 6: {
        // Pasting two paragraphs.
        const pasted = PMNode.fromJSON(schema, parse("pasted A\n\npasted B\n").doc);
        return (tr) => tr.replace(p, q, pasted.slice(1, pasted.content.size - 1));
      }
      default:
        return (tr) => {
          // Toggling a task item.
          const $p = tr.doc.resolve(p);
          for (let d = $p.depth; d > 0; d--)
            if ($p.node(d).type.name === "listItem") {
              tr.setNodeAttribute($p.before(d), "checked", $p.node(d).attrs.checked ? false : true);
              return;
            }
          tr.insertText("t", p);
        };
    }
  }

  // FUZZ_SEEDS and FUZZ_OPS run it longer.
  it.each(Array.from({ length: Number(process.env.FUZZ_SEEDS ?? 150) }, (_, i) => [i]))("seed %i", (seed) => {
    const r = rng(seed + 1);
    const original = stateOf(SAMPLE);
    const published = md(original, "reject");
    let s = original;
    const log: string[] = [];
    for (let i = 0; i < Number(process.env.FUZZ_OPS ?? 25); i++) {
      const op = randomOp(s.doc, r);
      log.push(op.desc!);
      // Against a clean page, accepting equals editing directly.
      if (i === 0) {
        const direct = original.tr;
        try {
          op(direct);
        } catch {
          continue;
        }
        const via = suggest(original, op, "u1");
        if (via && direct.docChanged) expect(md(via, "accept"), op.desc).toBe(serialize(direct.doc.toJSON() as DocNode).trim());
      }
      let next: EditorState | null;
      try {
        next = suggest(s, op, "u" + (1 + Math.floor(r() * 2)));
      } catch (e) {
        if (e instanceof RangeError) continue; // the op itself doesn't apply here
        throw e;
      }
      if (!next) continue;
      s = next;
      expect(md(s, "reject"), log.join(" ")).toBe(published);
      PMNode.fromJSON(schema, resolveSuggestions(s.doc.toJSON() as DocNode, () => "accept")).check();
    }
  });
});

// Spike S4, over Yjs: two editors suggest concurrently while the server
// accepts and rejects; every replica converges on the same valid document,
// and nothing is dropped by the editor schema on the way.
describe("suggesting over Yjs (fuzz)", () => {
  const SAMPLE = "# Title\n\nFirst paragraph with some words.\n\nSecond one here.\n\n- item one\n- item two\n\nLast line.\n";
  type J = { type: string; attrs?: Record<string, unknown>; marks?: Mark[]; content?: J[] };
  const norm = (n: J): J => ({ ...n, ...(n.marks ? { marks: sortMarks(n.marks) } : {}), ...(n.content ? { content: n.content.map(norm) } : {}) });
  const noSid = (json: string) => json.replace(/"sid":(null|\d+),?/g, "").replace(/,}/g, "}");

  function rng(seed: number) {
    return () => {
      seed = (seed + 0x6d2b79f5) | 0;
      let t = Math.imul(seed ^ (seed >>> 15), 1 | seed);
      t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
      return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
    };
  }

  function edit(state: EditorState, r: () => number, author: string): EditorState {
    const ps: number[] = [];
    state.doc.descendants((node, pos) => {
      if (node.inlineContent) for (let i = 0; i <= node.content.size; i++) ps.push(pos + 1 + i);
      return !node.inlineContent;
    });
    const p = ps[Math.floor(r() * ps.length)]!;
    const q = ps[Math.min(ps.length - 1, ps.indexOf(p) + 1 + Math.floor(r() * 8))]!;
    const k = Math.floor(r() * 4);
    try {
      return (
        suggest(state, (tr) => {
          if (k === 0) tr.insertText(author, p);
          else if (k === 1) tr.delete(p, q);
          else if (k === 2 && state.doc.resolve(p).parent.type.name === "paragraph") tr.split(p);
          else tr.insertText("Z", p, q);
        }, author) ?? state
      );
    } catch (e) {
      if (e instanceof RangeError) return state;
      throw e;
    }
  }

  it.each(Array.from({ length: Number(process.env.FUZZ_SEEDS ?? 60) }, (_, i) => [i]))("seed %i", (seed) => {
    const r = rng(seed + 7);
    const origin = new Y.Doc();
    writeDoc(origin.getXmlFragment(CONTENT), parse(SAMPLE).doc);
    const replica = (id: number) => {
      const y = new Y.Doc();
      y.clientID = id;
      Y.applyUpdate(y, Y.encodeStateAsUpdate(origin));
      return y;
    };
    const server = replica(1);
    const clients = [2, 3].map((id) => {
      const y = replica(id);
      const { doc, meta } = initProseMirrorDoc(y.getXmlFragment(CONTENT), schema);
      return { y, meta, author: "u" + id, state: EditorState.create({ schema, doc }) };
    });
    const all = [server, ...clients.map((c) => c.y)];
    for (let round = 0; round < 12; round++) {
      for (const c of clients) {
        for (let i = 0; i < 1 + Math.floor(r() * 3); i++) {
          c.state = edit(c.state, r, c.author);
          updateYFragment(c.y, c.y.getXmlFragment(CONTENT), c.state.doc, c.meta);
        }
      }
      // Meanwhile the server resolves some of what it has seen.
      const frag = server.getXmlFragment(CONTENT);
      const pending = listSuggestions(readDoc(frag));
      if (pending.length && r() < 0.5) {
        const pick = new Set(pending.filter(() => r() < 0.5).map((s) => s.id));
        const verdict = r() < 0.5 ? "accept" : "reject";
        server.transact(() => applyDoc(frag, resolveSuggestions(readDoc(frag), (s) => (pick.has(s.id) ? verdict : null)), nodeHash));
      }
      // Everyone syncs.
      for (const a of all) for (const b of all) if (a !== b) Y.applyUpdate(b, Y.encodeStateAsUpdate(a, Y.encodeStateVector(b)));
      const want = canonical(norm(readDoc(server.getXmlFragment(CONTENT)) as unknown as J));
      for (const c of clients) {
        expect(canonical(norm(readDoc(c.y.getXmlFragment(CONTENT)) as unknown as J))).toBe(want);
        const { doc, meta } = initProseMirrorDoc(c.y.getXmlFragment(CONTENT), schema);
        doc.check();
        // The editor schema kept everything the shared document holds.
        expect(noSid(canonical(norm(doc.toJSON() as J)))).toBe(noSid(want));
        c.state = EditorState.create({ schema, doc });
        c.meta = meta;
      }
      expect(() => serialize(readDoc(server.getXmlFragment(CONTENT)))).not.toThrow();
      expect(yXmlFragmentToProseMirrorRootNode(server.getXmlFragment(CONTENT), schema).childCount).toBeGreaterThan(0);
    }
  });
});
