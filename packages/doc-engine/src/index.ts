/**
 * @kmdn/doc-engine: the shared document engine (schema, markdown parse and
 * fidelity serializer, diff, merge). Runs in the browser and inside the Go
 * server. See docs/specs/04-doc-engine.md.
 */
export const ENGINE_VERSION = 1;

export * from "./schema";
export { parse, toMdast, type ParseResult } from "./parse";
export { serialize, serializeBlock } from "./serialize";
export { canonical, fnv1a64, nodeHash } from "./hash";
