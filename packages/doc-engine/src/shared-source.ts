import * as Y from "yjs";
import { canonical, nodeHash } from "./hash";
import { withoutConflicts } from "./merge";
import { parse, type ParseResult } from "./parse";
import { serialize, serializeBlock } from "./serialize";
import { defaultStyle, type BlockNode, type DocNode, type SourceMap } from "./schema";
import { withoutSuggestions } from "./suggestions";
import { CONTENT, readDoc } from "./ydoc";

export const SOURCE = "source";
export const MAX_SOURCE_BYTES = 1 << 20;
export const SOURCE_TOO_LARGE = "docengine: authored source exceeds input limit";

type Piece = { body: string; after: string; next: string };
type Snapshot = { leading: string; blocks: Map<string, Piece> };
type Projected = { block: BlockNode; owner?: string; legacySid?: number };

function utf8Length(text: string): number {
  let size = 0;
  for (const char of text) {
    const code = char.codePointAt(0)!;
    size += code < 0x80 ? 1 : code < 0x800 ? 2 : code < 0x10000 ? 3 : 4;
  }
  return size;
}

export function checkSourceSize(text: string, maxBytes = MAX_SOURCE_BYTES): void {
  if (text.length > maxBytes || utf8Length(text) > maxBytes) throw new RangeError(SOURCE_TOO_LARGE);
}

function sid(block: BlockNode): number | undefined {
  return (block as { attrs?: { sid?: number } }).attrs?.sid;
}

function withSid(block: BlockNode, value: number | undefined): BlockNode {
  return { ...block, attrs: { ...(block as { attrs?: object }).attrs, sid: value } } as BlockNode;
}

function semantic(block: BlockNode): string {
  return canonical(withSid(block, undefined));
}

/** Refers to the element itself; deleting it cannot retarget the next block. */
function ownerOf(el: Y.XmlElement | Y.XmlText | Y.XmlHook): string | undefined {
  const id = Y.createRelativePositionFromTypeIndex(el, 0).type;
  return id ? `block:${id.client}:${id.clock}` : undefined;
}

function projected(doc: Y.Doc): Projected[] {
  const fragment = doc.getXmlFragment(CONTENT);
  const original = readDoc(fragment);
  const elements = fragment.toArray();
  // Negative temporary ids survive suggestion resolution without colliding
  // with legacy source ids inside an unresolved conflict's revision side.
  const tagged: DocNode = { type: "doc", content: original.content.map((b, i) => withSid(b, -i - 1)) };
  return withoutConflicts(withoutSuggestions(tagged)).content.map((block) => {
    const ref = sid(block);
    if (ref !== undefined && ref < 0) {
      const index = -ref - 1;
      const element = elements[index];
      return { block, owner: element ? ownerOf(element) : undefined, legacySid: original.content[index] ? sid(original.content[index]!) : undefined };
    }
    return { block, legacySid: ref };
  });
}

/** Takes ranges only from our parser, never from shared metadata. */
export function sourceSnapshot(doc: Y.Doc, parsed: ParseResult): Snapshot {
  const current = projected(doc);
  const sm = parsed.sourceMap;
  const blocks = new Map<string, Piece>();
  parsed.doc.content.forEach((block, i) => {
    const live = current[i];
    const range = sm.blocks[i]!;
    if (!live?.owner || semantic(live.block) !== semantic(block)) return;
    blocks.set(live.owner, {
      body: sm.original.slice(range.start, range.end),
      after: sm.original.slice(range.end, sm.blocks[i + 1]?.start ?? sm.original.length),
      next: current[i + 1]?.owner ?? "",
    });
  });
  return { leading: (sm.bom ? "\ufeff" : "") + sm.leading, blocks };
}

function patchText(text: Y.Text, value: string): void {
  const before = text.toString();
  if (before === value) return;
  let start = 0;
  while (start < before.length && start < value.length && before[start] === value[start]) start++;
  const splitsPair = (text: string, at: number) => at > 0 && /[\uD800-\uDBFF]/.test(text[at - 1]!) && /[\uDC00-\uDFFF]/.test(text[at] ?? "");
  if (splitsPair(before, start) || splitsPair(value, start)) start--;
  let end = 0;
  while (end < before.length - start && end < value.length - start && before[before.length - 1 - end] === value[value.length - 1 - end]) end++;
  if (splitsPair(before, before.length - end) || splitsPair(value, value.length - end)) end--;
  if (before.length - start - end) text.delete(start, before.length - start - end);
  const insert = value.slice(start, value.length - end);
  if (insert) text.insert(start, insert);
}

function setText(map: Y.Map<unknown>, key: string, value: string): void {
  let text = map.get(key);
  if (!(text instanceof Y.Text)) {
    text = new Y.Text();
    map.set(key, text);
  }
  patchText(text as Y.Text, value);
}

/** Write only changed blocks: first edits to legacy docs also merge independently. */
export function writeSource(doc: Y.Doc, parsed: ParseResult, previous?: Snapshot): void {
  const next = sourceSnapshot(doc, parsed);
  const source = doc.getMap<unknown>(SOURCE);
  if (!previous || next.leading !== previous.leading) setText(source, "leading", next.leading);
  for (const [owner, piece] of next.blocks) {
    const old = previous?.blocks.get(owner);
    const existing = source.get(owner);
    if (old && old.body === piece.body && old.after === piece.after && (!(existing instanceof Y.Map) || old.next === piece.next)) continue;
    const record: Y.Map<unknown> = existing instanceof Y.Map ? existing : new Y.Map<unknown>();
    if (record !== existing) source.set(owner, record);
    setText(record, "body", piece.body);
    setText(record, "after", piece.after);
    if (record.get("next") !== piece.next) record.set("next", piece.next);
  }
  const owners = new Set(doc.getXmlFragment(CONTENT).toArray().map(ownerOf));
  for (const key of source.keys()) if (key !== "leading" && !owners.has(key)) source.delete(key);
}

function sharedPieces(doc: Y.Doc, maxBytes: number): { leading?: string; blocks: Map<string, Piece> } {
  const source = doc.getMap<unknown>(SOURCE);
  let bytes = 0;
  let metadataBytes = 0;
  const invalid = () => { throw new Error("docengine: invalid authored source metadata"); };
  const count = (text: string) => {
    if (text.length > 4 * maxBytes - metadataBytes) throw new RangeError(SOURCE_TOO_LARGE);
    const size = utf8Length(text);
    metadataBytes += size;
    if (metadataBytes > 4 * maxBytes) throw new RangeError(SOURCE_TOO_LARGE);
    return size;
  };
  const identity = (key: string) => /^block:\d{1,16}:\d{1,16}$/.test(key);
  const read = (value: unknown): string | undefined => {
    if (value === undefined) return undefined;
    if (!(value instanceof Y.Text)) return invalid();
    if (value.length > maxBytes - bytes) throw new RangeError(SOURCE_TOO_LARGE);
    // Authored source is plain text. Reject opaque embeds and formatting
    // attributes rather than letting their uncounted data enter the metadata.
    for (const part of value.toDelta()) if (typeof part.insert !== "string" || (part.attributes && Object.keys(part.attributes).length)) invalid();
    const text = value.toString();
    bytes += count(text);
    if (bytes > maxBytes) throw new RangeError(SOURCE_TOO_LARGE);
    return text;
  };
  const leading = read(source.get("leading"));
  const blocks = new Map<string, Piece>();
  for (const [key, value] of source) {
    count(key);
    if (key === "leading") continue;
    if (!identity(key) || !(value instanceof Y.Map) || value.size !== 3) { invalid(); continue; }
    for (const field of value.keys()) {
      count(field);
      if (field !== "body" && field !== "after" && field !== "next") invalid();
    }
    const body = read(value.get("body"));
    const after = read(value.get("after"));
    const next = value.get("next");
    if (body === undefined || after === undefined || typeof next !== "string") { invalid(); continue; }
    count(next);
    if (next !== "" && !identity(next)) invalid();
    blocks.set(key, { body, after, next });
  }
  return { leading, blocks };
}

/** The live tree decides content; independently parsed source only supplies spelling. */
export function serializeShared(doc: Y.Doc, base?: SourceMap, maxBytes = MAX_SOURCE_BYTES): string {
  const shared = sharedPieces(doc, maxBytes);
  if (shared.leading === undefined && !shared.blocks.size) return serialize(readDoc(doc.getXmlFragment(CONTENT)), base);
  const blocks = projected(doc);
  const style = base?.style ?? defaultStyle;
  const leading = shared.leading !== undefined && /^\ufeff?[\t \r\n]*$/.test(shared.leading) ? shared.leading : (base?.bom ? "\ufeff" : "") + (base?.leading ?? "");
  let candidate = leading;
  const ranges: { start: number; end: number }[] = [];
  blocks.forEach((live, i) => {
    const piece = live.owner ? shared.blocks.get(live.owner) : undefined;
    const original = live.legacySid !== undefined ? base?.blocks[live.legacySid] : undefined;
    let body = original && original.hash === nodeHash(live.block) ? base!.original.slice(original.start, original.end) : serializeBlock(live.block, style);
    const next = blocks[i + 1];
    let after = next ? style.lineEnding + style.lineEnding : (base?.trailing ?? style.lineEnding);
    if (original && next?.legacySid === live.legacySid! + 1) after = base!.original.slice(original.end, base!.blocks[next.legacySid]!.start);
    if (piece) {
      body = piece.body;
      if (piece.next === (next?.owner ?? "") && /^[\t \r\n]*$/.test(piece.after)) after = piece.after;
    }
    const start = candidate.length;
    candidate += body;
    ranges.push({ start, end: candidate.length });
    candidate += after;
  });
  checkSourceSize(candidate, maxBytes);
  const parsed = parse(candidate);
  const offset = parsed.sourceMap.bom ? 1 : 0;
  const byRange = new Map(parsed.sourceMap.blocks.map((range, i) => [`${range.start + offset}:${range.end + offset}`, i]));
  const content = blocks.map((live, i) => {
    const range = ranges[i]!;
    const index = byRange.get(`${range.start}:${range.end}`);
    const match = index !== undefined && semantic(parsed.doc.content[index]!) === semantic(live.block);
    return withSid(live.block, match ? index : undefined);
  });
  return serialize({ type: "doc", content }, parsed.sourceMap);
}
