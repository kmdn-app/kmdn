# Shared Markdown source

Status: proposed

## Context

[Issue #165](https://github.com/kmdn-app/kmdn/issues/165) reproduces source edits that disappear after reload because formatting changes do not change the shared semantic tree. The current document-engine specification keeps original bytes outside the collaborative document. Preserving source edits requires changing that invariant.

## Decision

We will store authored Markdown in shared Yjs text alongside the semantic tree. Source maps remain derived data, parsed independently in the browser and server. Stable references associate source blocks with their Yjs elements. The tree remains authoritative for semantic content, and source bytes may be reused only when their parsed semantics match the current block.

We will preserve concurrent edits to different blocks, bound metadata size, and keep compatibility with documents that have no shared source. We will test formatting-only edits, replica convergence, concurrent insertion/deletion, duplicate blocks, reload, and the Go host before release.

## Consequences

Source formatting becomes durable collaborative data. The existing fidelity and Y.Doc-layout specifications must change. The document holds additional bounded metadata and needs stricter validation at materialization. No REST API or database schema changes are required. A single last-writer-wins document snapshot is insufficient because it would lose independent concurrent formatting edits.
