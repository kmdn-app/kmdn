import { readFileSync, readdirSync, statSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import en from "./en.json";

function files(dir: string): string[] {
  return readdirSync(dir).flatMap((n) => {
    const p = join(dir, n);
    if (statSync(p).isDirectory()) return files(p);
    return /\.tsx?$/.test(n) && !/\.test\.tsx?$/.test(n) ? [p] : [];
  });
}

function has(key: string): boolean {
  let cur: unknown = en;
  for (const part of key.split(".")) {
    if (!cur || typeof cur !== "object") return false;
    const obj = cur as Record<string, unknown>;
    // Plurals live in key_one / key_other.
    cur = part in obj ? obj[part] : obj[`${part}_other`] !== undefined ? obj[`${part}_other`] : undefined;
  }
  return cur !== undefined;
}

describe("locale keys", () => {
  it("every literal t() key used in the app exists in en.json", () => {
    const missing: string[] = [];
    for (const f of files(join(import.meta.dirname, ".."))) {
      const src = readFileSync(f, "utf8");
      for (const m of src.matchAll(/\bt\(\s*"([a-zA-Z][\w]*(?:\.[\w]+)+)"(\s*,\s*\{[^}]*defaultValue)?/g)) {
        if (!m[2] && !has(m[1]!)) missing.push(`${m[1]} (${f.split("/src/")[1]})`);
      }
    }
    expect(missing).toEqual([]);
  });
});
