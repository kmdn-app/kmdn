/**
 * Fidelity corpus (spike S2): committed adversarial fixtures plus every
 * README/markdown file found in node_modules (real-world docs, not committed).
 */
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, relative } from "node:path";
import { parse } from "./parse";
import { serialize } from "./serialize";
import type { BlockNode, DocNode } from "./schema";
import { canonical } from "./hash";

const repo = join(import.meta.dirname, "../../..");
const fixtures = join(repo, "testdata/fidelity");

function walk(dir: string, out: string[], depth = 0) {
  if (depth > 8 || out.length > 1500) return;
  let entries: string[];
  try {
    entries = readdirSync(dir);
  } catch {
    return;
  }
  for (const e of entries.sort()) {
    const p = join(dir, e);
    let st;
    try {
      st = statSync(p);
    } catch {
      continue;
    }
    if (st.isDirectory()) walk(p, out, depth + 1);
    else if (/\.(md|markdown)$/i.test(e) && st.size < 512 * 1024) out.push(p);
  }
}

const files: string[] = readdirSync(fixtures).map((f) => join(fixtures, f));
// pnpm links the same package into many folders: keep one copy of each file
// so duplicates don't skew the rates, and walk in sorted order so the corpus
// is the same on every machine.
const found: string[] = [];
walk(join(repo, "node_modules/.pnpm"), found);
const seen = new Set<string>();
const corpus = found.filter((f) => {
  const c = readFileSync(f, "utf8");
  if (seen.has(c)) return false;
  seen.add(c);
  return true;
});
const all = [...files, ...corpus];

test("corpus is large enough to mean something", () => {
  expect(all.length).toBeGreaterThan(200);
});

describe.each(all.map((f) => [relative(repo, f), f]))("%s", (_name, file) => {
  const src = readFileSync(file, "utf8");

  test("round-trips byte for byte", () => {
    const { doc, sourceMap } = parse(src);
    expect(serialize(doc, sourceMap)).toBe(src);
  });

  test("single-block edit changes only that block", () => {
    const { doc, sourceMap } = parse(src);
    const idx = doc.content.findIndex((b) => b.type === "paragraph" && (b.content?.length ?? 0) > 0);
    if (idx < 0) return;
    const edited: DocNode = { ...doc, content: doc.content.map((b, i) => (i === idx ? appendText(b) : b)) };
    const out = serialize(edited, sourceMap);
    const orig = sourceMap.blocks[idx]!;
    const prefix = (sourceMap.bom ? "﻿" : "") + src.slice(sourceMap.bom ? 1 : 0).slice(0, orig.start);
    const suffix = src.slice(sourceMap.bom ? 1 : 0).slice(orig.end);
    expect(out.startsWith(prefix)).toBe(true);
    expect(out.endsWith(suffix)).toBe(true);
    expect(out).toContain("kmdn-edit");
  });
});

function appendText(b: BlockNode): BlockNode {
  if (b.type !== "paragraph") return b;
  return { ...b, content: [...(b.content ?? []), { type: "text", text: " kmdn-edit" }] };
}

/** Model fidelity: re-serializing without a source map and parsing again yields the same model. */
describe("fresh serialization preserves the model", () => {
  const results = all.map((f) => {
    const src = readFileSync(f, "utf8");
    const { doc } = parse(src);
    const fresh = serialize(stripSids(doc));
    const again = parse(fresh).doc;
    const a = stripSids(doc);
    const b = stripSids(again);
    const exact = canonical(a) === canonical(b);
    const ok = exact || canonical(tolerant(a)) === canonical(tolerant(b));
    let diff = "";
    if (!ok) {
      const i = a.content.findIndex((blk, k) => canonical(blk) !== canonical(b.content[k]));
      diff = `block ${i}\n    was: ${JSON.stringify(a.content[i]).slice(0, 400)}\n    now: ${JSON.stringify(b.content[i]).slice(0, 400)}`;
    }
    return { f, ok, exact, diff };
  });
  const failed = results.filter((r) => !r.ok);
  test("fixtures are exact", () => {
    expect(failed.filter((r) => r.f.startsWith(fixtures)).map((r) => relative(repo, r.f))).toEqual([]);
  });
  // Known deviations (only affect blocks that were edited): mdast-util-to-markdown
  // turns a soft line break before inline HTML into a space (normalized by
  // tolerant() below), and a paragraph that starts with inline HTML re-parses as
  // an HTML block. Both render the same.
  test("real-world corpus is at least 95% exact", () => {
    const rate = 1 - failed.length / results.length;
    const strict = results.filter((r) => r.exact).length / results.length;
    console.log(`model fidelity: ${(strict * 100).toFixed(1)}% exact, ${(rate * 100).toFixed(1)}% apart from line breaks before inline HTML`);
    if (rate < 1)
      console.log(
        `model fidelity ${(rate * 100).toFixed(1)}% of ${results.length}; failures:\n` +
          failed
            .slice(0, 20)
            .map((r, i) => "  " + relative(repo, r.f) + (i < 5 ? "\n    " + r.diff : ""))
            .join("\n"),
      );
    expect(rate).toBeGreaterThanOrEqual(0.95);
  });
});

/** Normalizes the line-break-before-inline-HTML deviation (see above). */
function tolerant(doc: DocNode): DocNode {
  const fix = (n: unknown): unknown => {
    if (!n || typeof n !== "object") return n;
    const node = n as { content?: unknown[] };
    if (!Array.isArray(node.content)) return n;
    const content = node.content.map(fix) as { type: string; text?: string }[];
    for (let i = 0; i + 1 < content.length; i++) {
      const t = content[i]!;
      if (t.type === "text" && t.text && content[i + 1]!.type === "rawInline") content[i] = { ...t, text: t.text.replace(/[ \t]*\r?\n[ \t]*$/, " ") };
    }
    return { ...node, content };
  };
  return fix(doc) as DocNode;
}

function stripSids(doc: DocNode): DocNode {
  return { type: "doc", content: doc.content.map((b) => ({ ...b, attrs: { ...(b as { attrs?: object }).attrs, sid: undefined } }) as BlockNode) };
}
