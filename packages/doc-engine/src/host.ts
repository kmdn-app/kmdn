/**
 * Entry point for the Go host (internal/docengine). Exposes the engine on
 * globalThis.kmdn with string/ArrayBuffer-in, string/ArrayBuffer-out
 * functions so the bridge only moves plain values across the runtime
 * boundary. The server keeps no Y.Doc between calls: rooms hold encoded
 * updates in Go and these functions do the CRDT math on demand.
 */
import "./shim";
import * as Y from "yjs";
import { ENGINE_VERSION } from "./index";
import { parse } from "./parse";
import { serialize } from "./serialize";
import { CONTENT, applyDoc, readDoc, writeDoc } from "./ydoc";
import { nodeHash } from "./hash";
import { extractLinks, rewriteLinks } from "./links";
import type { DocNode, SourceMap } from "./schema";

const u8 = (b: ArrayBuffer) => new Uint8Array(b);
const buf = (a: Uint8Array): ArrayBuffer => a.buffer.slice(a.byteOffset, a.byteOffset + a.byteLength) as ArrayBuffer;

function load(update: ArrayBuffer): Y.Doc {
  const d = new Y.Doc({ gc: true });
  Y.applyUpdate(d, u8(update));
  return d;
}

const api = {
  version: () => ENGINE_VERSION,
  /** markdown → JSON {doc, sourceMap} */
  parse: (markdown: string): string => JSON.stringify(parse(markdown)),
  /** JSON doc (+ optional JSON sourceMap) → markdown */
  serialize: (docJSON: string, sourceMapJSON: string): string =>
    serialize(JSON.parse(docJSON) as DocNode, sourceMapJSON ? (JSON.parse(sourceMapJSON) as SourceMap) : undefined),
  /** markdown → markdown (parse + serialize), used as a health check */
  roundTrip: (markdown: string): string => {
    const r = parse(markdown);
    return serialize(r.doc, r.sourceMap);
  },

  /**
   * A new collaborative document for a page: returns the full state as an
   * update and the source map for materializing it later.
   */
  yFromMarkdown: (markdown: string, clientID: number): { update: ArrayBuffer; sourceMap: string } => {
    const { doc, sourceMap } = parse(markdown);
    const d = new Y.Doc();
    d.clientID = clientID;
    d.transact(() => {
      writeDoc(d.getXmlFragment(CONTENT), doc);
      d.getMap("meta").set("engineVersion", ENGINE_VERSION);
    });
    return { update: buf(Y.encodeStateAsUpdate(d)), sourceMap: JSON.stringify(sourceMap) };
  },
  /** state → markdown, reusing untouched bytes via the source map */
  yMaterialize: (update: ArrayBuffer, sourceMapJSON: string): string => {
    const d = load(update);
    const sm = sourceMapJSON ? (JSON.parse(sourceMapJSON) as SourceMap) : undefined;
    return serialize(readDoc(d.getXmlFragment(CONTENT)), sm);
  },
  /**
   * The update that turns state into `markdown`, written by clientID (edits
   * made outside an editor). Returns an empty update when nothing changed.
   */
  yApplyMarkdown: (update: ArrayBuffer, markdown: string, clientID: number): ArrayBuffer => {
    const d = load(update);
    d.clientID = clientID;
    // The transaction's own update: encodeStateAsUpdate(d, sv) would also carry
    // the document's whole delete set.
    let out: Uint8Array = new Uint8Array([0, 0]);
    d.on("update", (u: Uint8Array) => {
      out = u;
    });
    d.transact(() => applyDoc(d.getXmlFragment(CONTENT), parse(markdown).doc, nodeHash));
    return buf(out);
  },
  /** markdown → JSON {links, headings} */
  links: (markdown: string): string => JSON.stringify(extractLinks(markdown)),
  /** markdown + JSON {oldUrl: newUrl} → markdown with those destinations replaced */
  rewriteLinks: (markdown: string, mapJSON: string): string => {
    const map = JSON.parse(mapJSON) as Record<string, string>;
    return rewriteLinks(markdown, (u) => map[u] ?? null);
  },
  /**
   * Surviving content per Yjs client (characters and embedded nodes that are
   * still in the document): who wrote what, for Co-authored-by.
   */
  yContributions: (update: ArrayBuffer): string => {
    const d = load(update);
    const counts: Record<string, number> = {};
    d.store.clients.forEach((structs, client) => {
      let n = 0;
      for (const s of structs) if (s instanceof Y.Item && !s.deleted && s.countable) n += s.length;
      if (n > 0) counts[String(client)] = n;
    });
    return JSON.stringify(counts);
  },
  /** merge updates into one (compaction) */
  yMerge: (updates: ArrayBuffer[]): ArrayBuffer => buf(Y.mergeUpdates(updates.map(u8))),
  /** what a peer with state vector sv is missing */
  yDiff: (update: ArrayBuffer, sv: ArrayBuffer): ArrayBuffer => buf(Y.diffUpdate(u8(update), u8(sv))),
  yStateVector: (update: ArrayBuffer): ArrayBuffer => buf(Y.encodeStateVectorFromUpdate(u8(update))),
  /** Validates an update and returns the client ids that wrote it. Throws on garbage. */
  yClients: (update: ArrayBuffer): number[] => {
    const meta = Y.parseUpdateMeta(u8(update));
    return [...meta.to.keys()];
  },
};

(globalThis as unknown as { kmdn: typeof api }).kmdn = api;
