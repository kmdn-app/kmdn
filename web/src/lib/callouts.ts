export type Callout = { type: string; title?: string; body: string };

/** GitHub alert (`> [!NOTE] optional title`) from a raw blockquote. */
export function parseAlert(raw: string): Callout {
  const lines = raw.split("\n").map((l) => l.replace(/^[ \t]*>[ \t]?/, ""));
  const m = lines[0]!.match(/^\[!(\w+)\][ \t]*(.*)$/);
  return { type: (m?.[1] ?? "note").toLowerCase(), title: m?.[2]?.trim() || undefined, body: lines.slice(1).join("\n") };
}

/** Directive container (`::: warning Title` … `:::`). */
export function parseContainer(raw: string): Callout {
  const lines = raw.split("\n");
  const [type = "note", ...title] = lines[0]!.replace(/^:{3,}\s*/, "").split(/\s+/);
  const end = /^:{3,}\s*$/.test(lines[lines.length - 1] ?? "") ? lines.length - 1 : lines.length;
  return { type: type.toLowerCase() || "note", title: title.join(" ") || undefined, body: lines.slice(1, end).join("\n") };
}
