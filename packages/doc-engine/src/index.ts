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
export { CONTENT, applyDoc, readDoc, writeDoc } from "./ydoc";
export { extractLinks, rewriteLinks, slugify, type Heading, type Link, type LinkKind } from "./links";
export { SourceSync, textChange } from "./source-sync";
export { diff3, hasConflicts, lcs, merge3, withoutConflicts, type Chunk, type MergeResult } from "./merge";
export { suggestEdit, type EditAttrs } from "./suggest-edit";
export {
  hasSuggestions,
  listSuggestions,
  resolveSuggestions,
  settledHash,
  withoutSuggestions,
  type Decide,
  type Decision,
  type NodeSuggestion,
  type SuggestionAttrs,
  type SuggestionInfo,
  type SuggestionKind,
} from "./suggestions";
