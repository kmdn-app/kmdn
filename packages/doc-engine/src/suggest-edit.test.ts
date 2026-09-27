import { describe, expect, it } from "vitest";
import { parse } from "./parse";
import { serialize } from "./serialize";
import { listSuggestions, resolveSuggestions } from "./suggestions";
import { suggestEdit } from "./suggest-edit";
import type { DocNode } from "./schema";

const A = { id: "s1", author: "u1", assistant: true };
const md = (d: DocNode, v: "accept" | "reject") => serialize(resolveSuggestions(d, () => v));

function check(before: string, after: string) {
  const d = suggestEdit(parse(before).doc, parse(after).doc, A);
  expect(md(d, "reject")).toBe(serialize(parse(before).doc));
  expect(md(d, "accept")).toBe(serialize(parse(after).doc));
  return d;
}

describe("suggestEdit", () => {
  it("records a word change inside a paragraph", () => {
    const d = check("# Policy\n\nLaptops every three years.\n", "# Policy\n\nLaptops every four years.\n");
    const s = listSuggestions(d);
    expect(s).toHaveLength(1);
    expect(s[0]).toMatchObject({ id: "s1", author: "u1" });
    expect(s[0]!.deleted).toBe("three");
    expect(s[0]!.inserted).toBe("four");
  });

  it("adds and removes whole blocks", () => {
    check("# A\n\nOne.\n\nTwo.\n", "# A\n\nOne.\n\nNew paragraph.\n\nTwo.\n\n- a list\n- of things\n");
    check("# A\n\nOne.\n\nTwo.\n\n- x\n- y\n", "# A\n\nTwo.\n");
  });

  it("edits inside lists and code blocks, and changes heading levels", () => {
    check("- one\n- two\n\n```\nlet a = 1\n```\n", "- one\n- two, and more\n- three\n\n```\nlet a = 2\n```\n");
    check("## Title\n\nText.\n", "### Title\n\nText.\n");
  });

  it("keeps blocks it doesn't change as they are, suggestions included", () => {
    const cur = parse("Keep me.\n\nEdit me.\n").doc;
    const pending = { ...cur, content: [{ ...cur.content[0]!, content: [{ type: "text", text: "Keep me." }, { type: "text", text: " (pending)", marks: [{ type: "insertion", attrs: { id: "p1", author: "u2" } }] }] }, cur.content[1]!] } as DocNode;
    const d = suggestEdit(pending, parse("Keep me.\n\nEdited.\n").doc, A);
    expect(listSuggestions(d).map((s) => s.id).sort()).toEqual(["p1", "s1"]);
    expect(md(d, "reject")).toBe("Keep me.\n\nEdit me.\n");
  });

  it("marks inserted text as the assistant's", () => {
    const d = suggestEdit(parse("A.\n").doc, parse("A. B.\n").doc, A);
    const t = (d.content[0] as { content: { text: string; marks?: { type: string; attrs: { assistant?: boolean } }[] }[] }).content.find((n) => n.marks?.length);
    expect(t?.marks?.[0]).toMatchObject({ type: "insertion", attrs: { assistant: true } });
  });
});

describe("suggestEdit (fuzz)", () => {
  const SAMPLE = ["# Guide", "", "Intro paragraph with some words.", "", "## Setup", "", "- install the tools", "- run the setup", "", "```", "make build", "```", "", "> A quote to keep.", "", "Last line here."];
  function rng(seed: number) {
    return () => {
      seed = (seed + 0x6d2b79f5) | 0;
      let t = Math.imul(seed ^ (seed >>> 15), 1 | seed);
      t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
      return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
    };
  }
  const WORDS = ["alpha", "beta", "new", "changed", "more", "text", "and"];
  it.each(Array.from({ length: 200 }, (_, i) => [i]))("seed %i", (seed) => {
    const r = rng(seed + 11);
    const lines = [...SAMPLE];
    for (let n = 1 + Math.floor(r() * 4); n > 0; n--) {
      const i = Math.floor(r() * lines.length);
      const k = r();
      if (k < 0.4 && lines[i]) {
        const ws = lines[i]!.split(" ");
        ws[Math.floor(r() * ws.length)] = WORDS[Math.floor(r() * WORDS.length)]!;
        lines[i] = ws.join(" ");
      } else if (k < 0.6) lines.splice(i, 0, "", "Inserted " + WORDS[Math.floor(r() * WORDS.length)] + ".", "");
      else if (k < 0.8) lines.splice(i, 1);
      else lines.splice(i, 0, "- extra item");
    }
    const before = SAMPLE.join("\n") + "\n";
    const after = lines.join("\n") + "\n";
    const d = suggestEdit(parse(before).doc, parse(after).doc, A);
    expect(md(d, "reject")).toBe(serialize(parse(before).doc));
    expect(md(d, "accept")).toBe(serialize(parse(after).doc));
  });
});
