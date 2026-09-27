import { describe, expect, it } from "vitest";
import { diff3, merge3, withoutConflicts } from "./merge";
import { parse } from "./parse";
import { serialize } from "./serialize";

const BASE = "# Guide\n\nIntro paragraph.\n\n## Setup\n\nInstall the tools.\n\n## Usage\n\nRun it.\n";

describe("diff3", () => {
  it("merges changes to neighbouring items and flags clashes", () => {
    const k = (x: string) => x;
    expect(diff3(["a", "b", "c"], ["a", "B", "c"], ["a", "b", "C"], k)).toEqual([
      { kind: "same", items: ["a"] },
      { kind: "clean", items: ["B"] },
      { kind: "clean", items: ["C"] },
    ]);
    const chunks = diff3(["a", "b"], ["a", "x"], ["a", "y"], k);
    expect(chunks).toEqual([
      { kind: "same", items: ["a"] },
      { kind: "conflict", base: ["b"], ours: ["x"], theirs: ["y"] },
    ]);
  });
});

describe("merge3", () => {
  const md = (r: { doc: Parameters<typeof serialize>[0] }) => serialize(r.doc);

  it("brings in Published's changes to other blocks", () => {
    const ours = BASE.replace("Install the tools.", "Install the tools with brew.");
    const theirs = BASE.replace("Run it.", "Run it daily.");
    const r = merge3(BASE, ours, theirs);
    expect(r.conflicts).toBe(0);
    expect(md(r)).toBe(BASE.replace("Install the tools.", "Install the tools with brew.").replace("Run it.", "Run it daily."));
  });

  it("keeps the revision's bytes where Published didn't change anything", () => {
    const ours = "# Guide\n\nIntro   paragraph.\n\n* one\n* two\n";
    const theirs = "# Guide\n\nIntro   paragraph.\n\n* one\n* two\n\nMore.\n";
    const base = "# Guide\n\nIntro   paragraph.\n\n* one\n* two\n";
    const r = merge3(base, ours, theirs);
    expect(serialize(r.doc, parse(ours).sourceMap)).toBe("# Guide\n\nIntro   paragraph.\n\n* one\n* two\n\nMore.\n");
  });

  it("merges edits to different lines of the same block", () => {
    const base = "```\nline one\nline two\nline three\n```\n";
    const ours = "```\nline ONE\nline two\nline three\n```\n";
    const theirs = "```\nline one\nline two\nline THREE\n```\n";
    const r = merge3(base, ours, theirs);
    expect(r.conflicts).toBe(0);
    expect(md(r)).toBe("```\nline ONE\nline two\nline THREE\n```\n");
  });

  it("turns a real clash into a conflict showing both versions", () => {
    const ours = BASE.replace("Run it.", "Run it with care.");
    const theirs = BASE.replace("Run it.", "Run it twice.");
    const r = merge3(BASE, ours, theirs);
    expect(r.conflicts).toBe(1);
    const c = r.doc.content.find((b) => (b.type as string) === "conflict") as unknown as { content: { attrs: { side: string }; content: unknown[] }[] };
    expect(c.content.map((s) => s.attrs.side)).toEqual(["published", "revision"]);
    expect(serialize({ type: "doc", content: c.content[0]!.content } as never)).toBe("Run it twice.\n");
    // Until resolved, the page shows the revision's side.
    expect(md(r)).toBe(ours);
    expect(withoutConflicts(r.doc).content.some((b) => (b.type as string) === "conflict")).toBe(false);
  });

  it("flags a block deleted here but edited on Published", () => {
    const ours = BASE.replace("## Usage\n\nRun it.\n", "").replace(/\n+$/, "\n");
    const theirs = BASE.replace("Run it.", "Run it daily.");
    const r = merge3(BASE, ours, theirs);
    expect(r.conflicts).toBe(1);
  });

  it("merges a page both sides created", () => {
    const r = merge3("", "# New\n\nOurs.\n", "# New\n\nTheirs.\n");
    expect(r.conflicts).toBe(1);
    expect(md(r)).toBe("# New\n\nOurs.\n");
  });
});

describe("diff3 (properties)", () => {
  function rng(seed: number) {
    return () => {
      seed = (seed + 0x6d2b79f5) | 0;
      let t = Math.imul(seed ^ (seed >>> 15), 1 | seed);
      t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
      return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
    };
  }
  const mutate = (xs: string[], r: () => number, tag: string) => {
    const out = [...xs];
    for (let n = Math.floor(r() * 3); n >= 0; n--) {
      const i = Math.floor(r() * (out.length + 1));
      const k = r();
      if (k < 0.33 && out.length) out.splice(Math.min(i, out.length - 1), 1);
      else if (k < 0.66) out.splice(i, 0, tag + i);
      else if (out.length) out[Math.min(i, out.length - 1)] = tag + "!" + i;
    }
    return out;
  };
  it.each(Array.from({ length: 300 }, (_, i) => [i]))("seed %i", (seed) => {
    const r = rng(seed + 3);
    const base = Array.from({ length: 1 + Math.floor(r() * 8) }, (_, i) => "b" + i);
    const ours = mutate(base, r, "o");
    const theirs = mutate(base, r, "t");
    const chunks = diff3(base, ours, theirs, (x) => x);
    const pick = (side: "ours" | "theirs") => chunks.flatMap((c) => (c.kind === "conflict" ? c[side] : c.items));
    // Taking a side in every conflict gives back...
    if (!chunks.some((c) => c.kind === "conflict")) {
      // ...a result containing every change: nothing either side added is lost.
      const merged = pick("ours");
      for (const x of [...ours, ...theirs]) if (!base.includes(x)) expect(merged).toContain(x);
    }
    // One side unchanged: the merge is the other side.
    expect(diff3(base, base, theirs, (x) => x).flatMap((c) => (c.kind === "conflict" ? [] : c.items))).toEqual(theirs);
    expect(diff3(base, ours, base, (x) => x).flatMap((c) => (c.kind === "conflict" ? [] : c.items))).toEqual(ours);
  });
});
