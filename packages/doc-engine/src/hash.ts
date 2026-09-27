/**
 * Deterministic content hashing, identical in every JS host (browser, goja,
 * QuickJS): canonical JSON with sorted keys, then 64-bit FNV-1a over UTF-16
 * code units, hex encoded.
 */

export function canonical(value: unknown): string {
  if (value === null || typeof value !== "object") return JSON.stringify(value) ?? "null";
  if (Array.isArray(value)) return "[" + value.map(canonical).join(",") + "]";
  const obj = value as Record<string, unknown>;
  const keys = Object.keys(obj)
    .filter((k) => obj[k] !== undefined)
    .sort();
  return "{" + keys.map((k) => JSON.stringify(k) + ":" + canonical(obj[k])).join(",") + "}";
}

// FNV-1a 64-bit using two 32-bit halves (no BigInt, for older engines).
export function fnv1a64(s: string): string {
  let h0 = 0x8422_2325; // low 32 of offset basis 0xcbf29ce484222325
  let h1 = 0xcbf2_9ce4; // high 32
  for (let i = 0; i < s.length; i++) {
    h0 = (h0 ^ s.charCodeAt(i)) >>> 0;
    // multiply (h1:h0) by FNV prime 0x100000001b3 = 2^40 + 0x1b3
    const lo = h0 * 0x1b3;
    const carry = Math.floor(lo / 0x1_0000_0000);
    const nh0 = lo >>> 0;
    const nh1 = (h1 * 0x1b3 + carry + ((h0 << 8) >>> 0)) >>> 0; // + (h0 * 2^40) >> 32 == h0 << 8
    h0 = nh0;
    h1 = nh1;
  }
  return (h1 >>> 0).toString(16).padStart(8, "0") + (h0 >>> 0).toString(16).padStart(8, "0");
}

/** Hash of a node's content, ignoring its source id. */
export function nodeHash(node: unknown): string {
  return fnv1a64(canonical(stripSid(node)));
}

function stripSid(node: unknown): unknown {
  if (!node || typeof node !== "object") return node;
  const n = node as { attrs?: Record<string, unknown> };
  if (!n.attrs || !("sid" in n.attrs)) return node;
  const { sid: _sid, ...rest } = n.attrs;
  return { ...n, attrs: rest };
}
