# 04 · Document engine

`packages/doc-engine` is the heart of kmdn. It is one TypeScript package that runs in two hosts:

- **Browser**: inside the Tiptap editor, source mode, diff views.
- **Go server**: bundled as `internal/docengine/engine.js` (ES2020 IIFE, no DOM, no Node APIs, committed and checked in CI) and executed by [goja](https://github.com/dop251/goja) (spike S1 in [14](14-roadmap.md)). Interpreted JS is ~30× slower than V8, so the server parses only when needed and caches results by content hash.

Same code on both sides means one parser, one serializer, one diff, byte-identical results.

## Responsibilities

| Function | Used by |
|----------|---------|
| `parse(markdown) → {doc: PMJSON, sourceMap}` | Opening a file into a revision, source-mode reparse, auto-sync |
| `serialize(doc, original?, sourceMap?) → markdown` | Materializing revisions for diff, publish, source mode |
| `ydocFromMarkdown(md) → Uint8Array` (Yjs update) | Seeding a room |
| `applyUpdate(state, update) → state` / `encodeState` | Server-side room persistence |
| `diffMarkdown(a, b) → blockDiff` | Rendered diff, review, auto-sync |
| `merge3(base, ours, theirs) → {merged, conflicts[]}` | Auto-sync |
| `extractLinks(md) → Link[]`, `rewriteLinks(md, map)` | Link checker, rename rewriting |
| `extractText(md) → {title, headings[], text}` | Search index, assistant tools |
| `applyMarkdownPatch(ydoc, newMarkdown)` | Source mode edits, assistant edits |

## Schema

Tiptap/ProseMirror schema, one node per markdown construct so everything round-trips:

- **Blocks**: `doc`, `frontmatter` (single, first child only), `paragraph`, `heading(level)`, `blockquote`, `bulletList`/`orderedList(start, delimiter)`/`listItem`, `taskList`/`taskItem(checked)`, `codeBlock(lang, fence, meta)`, `table`/`tableRow`/`tableHeader`/`tableCell(align)`, `horizontalRule(marker)`, `image` (block-level when alone in a paragraph), `mermaid(source)`, `mathBlock(source)`, `footnoteDefinition(label)`, `htmlBlock(raw)`, **`rawBlock(raw, kind)`**.
- **Inlines**: `text`, `hardBreak(style)`, `image(src, alt, title)`, `footnoteReference(label)`, `mathInline(source)`, **`rawInline(raw)`**.
- **Marks**: `bold(marker)`, `italic(marker)`, `strike`, `code`, `link(href, title, style: inline|reference|autolink)`, plus review marks `insertion(suggestionId, author)` and `deletion(suggestionId, author)`.

Attributes record the source *style* (e.g. `*` vs `_`, `-` vs `*` bullets, fence length, reference-style links) so the serializer can reproduce it.

## Markdown fidelity (the core requirement)

**Invariant:** `serialize(parse(md)) === md` for every file in the fidelity corpus, and a local edit changes only the bytes of the blocks it touched.

Approach:

1. **Parser**: `micromark` + `mdast-util-from-markdown` with GFM, frontmatter, math, footnotes extensions, producing mdast with positions. mdast → ProseMirror JSON with each top-level block carrying `src: {start, end}` into the original text and a content hash.
2. **Source map kept outside the CRDT**: the original markdown bytes and per-block hashes live with the revision file state (server) and the client session. They are not collaborative data.
3. **Block-level reuse serializer**: for each top-level block, if its ProseMirror content hash equals the hash computed at parse time, emit the original bytes verbatim (including its trailing blank lines). Otherwise serialize that block with style-preserving rules (use the recorded markers, the file's dominant list marker, indentation and line width conventions detected at parse time). Inter-block whitespace is preserved from the original where blocks are unchanged.
4. **Nested granularity** (planned): for long lists and tables, reuse at list-item / table-row level so editing one item doesn't reflow the list. v1 of the engine reuses at top-level block granularity; ::: containers and GitHub alerts are kept as raw blocks.
5. **Normalization only on changed content**: e.g. a changed table re-pads columns only if the original table was padded.
6. **Line endings, final newline, BOM, trailing whitespace** of the file are detected and preserved.

The fidelity corpus (`testdata/fidelity/`) includes real-world READMEs and docs from Docusaurus, Hugo, MkDocs, VitePress, GitHub wikis, plus adversarial cases. CI runs round-trip and single-edit-locality tests over it in both hosts (Node for dev speed, the Go host for truth).

## Raw blocks and inlines

Anything the schema can't represent faithfully becomes `rawBlock`/`rawInline` holding the exact source:

- HTML blocks that aren't simple (the plain `htmlBlock` is also raw-ish but rendered sanitized as preview)
- Hugo/Jekyll shortcodes and Liquid (`{{< … >}}`, `{% … %}`)
- MDX-like JSX in `.md`, `:::` containers, GitHub alerts `> [!NOTE]` (v1 renders them as a raw block with a label; native callouts are post-v1)
- Link reference definitions, HTML comments, unusual setext/indent constructs that the serializer can't reproduce

In WYSIWYG, a raw block renders as a grey protected card labeled "Raw markdown · <kind>" showing the source in monospace, editable inline with a small CodeMirror. It round-trips byte-exact unless edited.

`.mdx` files open in **source mode only** in v1.

## Frontmatter

YAML frontmatter (`---` fenced, first line) is a `frontmatter` node rendered as a **Properties panel**: key/value rows with inferred types (string, number, boolean, date, list). Editing a value rewrites only that key's line(s) using a YAML CST (`yaml` package's `Document` API preserves comments, quoting style and order). Unsupported structures (nested maps beyond 1 level, anchors) show "Edit as YAML" which opens the frontmatter in a code editor. TOML frontmatter (`+++`) is kept as a raw block.

## Mermaid and math

- `mermaid` code fences become a `mermaid` node: rendered diagram, click to edit source in a split popover with live preview. Mermaid is lazy-loaded.
- `$…$` and `$$…$$` (remark-math syntax) become math nodes rendered by KaTeX, click to edit.
- Both serialize back to their exact original fence/delimiters.

## Source mode (live, bidirectional)

The Yjs document is the ProseMirror tree (`Y.XmlFragment`). Source mode is a per-user *view*:

1. On entering source mode, the client serializes the current doc to markdown and loads it into CodeMirror 6.
2. **Local edits**: debounced (300 ms idle, 1 s max). The client runs `parse` on the new text, computes a minimal tree diff against the current ProseMirror doc (block-level first, then within changed blocks via `prosemirror-changeset`-style text diff), and applies it as a ProseMirror transaction through `y-prosemirror`. Blocks that don't parse to a stable structure (half-typed table, unclosed fence) are applied as a temporary `rawBlock` and promoted once they parse.
3. **Remote edits**: on Yjs updates, the client re-serializes (only changed blocks, using the reuse serializer) and applies a text diff to CodeMirror, mapping the local selection through it.
4. **Presence**: remote cursors in source mode are mapped from ProseMirror positions to markdown offsets via the block source map; approximate inside re-serialized blocks.
5. **Suggesting in source mode**: edits made while Suggesting is on are converted to insertion/deletion marks on the resulting tree diff.

Risk: cursor jumps under heavy concurrent edits in the same block. Mitigation: never rewrite the block that contains the local cursor while the user is actively typing (within the debounce window); apply it after.

## Server host (Go ↔ JS bridge)

- `internal/docengine` exposes typed Go functions (`Parse`, `Serialize`, `Diff`, `Merge3`, `ApplyUpdate`, …) that marshal JSON/bytes to the JS runtime.
- Yjs binary state passes as `Uint8Array` (goja `ArrayBuffer` / wasm memory copy).
- Runtimes are created from a precompiled bundle (goja `Program` / QuickJS bytecode) for fast startup.
- Every call has a CPU budget and memory cap; exceeding them returns an error and marks the input for inspection.
- The bundle version is stamped; Y.Doc snapshots record the engine version that wrote them for future migrations.

## Link index and graph

Per repo, on every mirror update and revision change, `extractLinks` feeds a `links` table (`from_path`, `to_path`, `anchor`, `kind`). Used for:

- **Broken link checker**: in a revision, relative links and `#anchors` are resolved against the revision's virtual tree (published tree + revision changes). Absolute site routes resolve via `.kmdn.yml` `routes:` mappings (`/docs/` → `docs/`). External URLs are not checked in v1.
- **Link rewriting on rename/move**: find inbound links to the moved path, compute rewrites, add them to the revision as suggestions ("Update 3 links to this page").
- **Links tab** (right panel): for the current page, a local graph (the page centered, 1–2 hops), "Linked from" (backlinks) and "Links to" lists, broken links flagged. In a revision, links the revision adds are marked new and links it breaks are marked broken.
- **Graph page** (per repo): the whole content root as a force-directed graph (d3-force, computed in the browser; positions cached per user). Nodes colored by top-level folder, sized by inbound links; orphan pages (no inbound links) drawn dashed and listed; broken links as red dashed edges. Filters: folder, search, "Published | This revision", "Show duplicates" (dotted edges from the consistency scan). Clicking a node opens a card (title, path, in/out counts, Open). Served by `GET /repos/{repo}/graph` from the `links` table; no AI needed. Above ~2,000 pages the graph clusters by folder until zoomed in.
