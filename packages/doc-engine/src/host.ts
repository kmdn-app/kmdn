/**
 * Entry point for the Go host (internal/docengine). Exposes the engine on
 * globalThis.kmdn with string-in/string-out functions so the bridge only moves
 * strings across the runtime boundary.
 */
import { ENGINE_VERSION } from "./index";
import { parse } from "./parse";
import { serialize } from "./serialize";
import type { DocNode, SourceMap } from "./schema";

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
};

(globalThis as unknown as { kmdn: typeof api }).kmdn = api;
