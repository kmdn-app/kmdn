# 05 · Collaboration

## Units of collaboration

- One **Y.Doc per revision file** (`revision_id`, `path`). Keeping docs per file bounds memory and lets clients subscribe only to the files they open.
- A **revision manifest** (file list, renames, deletions, new files, assets) is regular DB state, not CRDT. File operations are REST calls broadcast as events.
- Files the revision hasn't touched have no Y.Doc. Opening one read-only shows published content; the first edit creates the Y.Doc from the published bytes (`ydocFromMarkdown`) and adds the file to the manifest as Modified.

## Y.Doc layout

```
Y.Doc
├─ "content"    Y.XmlFragment   ProseMirror tree (y-prosemirror)
├─ "comments"   Y.Map<threadId, {anchor: RelativePositionRange}>   anchors only; bodies in DB
└─ "meta"       Y.Map           { baseSha, engineVersion }
```

Comment bodies, authors and resolution live in the DB (queryable, notifiable). Only the anchor positions live in the CRDT so they move with the text.

## WebSocket protocol

One connection per tab: `wss://host/ws` (session cookie auth, `Origin` check). Frames are binary, each prefixed by a channel header:

```
[u8 kind][u32 channelId][payload]
kind: 0 control (JSON) · 1 yjs-sync · 2 yjs-awareness · 3 event (JSON) · 4 agent-stream (JSON)
```

Control messages (JSON):

- `{"op":"subscribe","channel":7,"room":{"revision":"d_123","path":"docs/a.md"}}` → server checks permission, replies `{"op":"subscribed","channel":7,"mode":"rw"|"ro"}` then runs y-protocols sync step 1/2.
- `{"op":"unsubscribe","channel":7}`
- `{"op":"subscribe-events","scope":"repo:r_1"|"revision:d_123"|"user"}` for revision state changes, manifest changes, comments, notifications, sync/conflict events.
- Heartbeat ping/pong every 20 s; server drops idle connections after 60 s.

Read-only subscribers get sync step 2 and updates but their updates are rejected. Who is read-only is decided per revision state ([07](07-review.md#who-can-do-what-by-state)): Viewers and non-invited Contributors always; revision editors while In review or Approved; everyone except assigned reviewers during review. When the state changes, the server sends `{"op":"mode","channel":7,"mode":"ro"|"rw","reason":"in_review"}` and clients switch the editor without reloading. Awareness from read-only users is accepted (they appear in presence as viewers).

## Server-side room

Rooms live in Go (`internal/collab`) and hold the document as encoded Yjs updates: a merged state plus the updates received since. The CRDT math runs in the doc engine's goja pool on demand (`yMerge`, `yDiff`, `yStateVector`, `yClients`, `yMaterialize`, `yApplyMarkdown`), so no JS runtime is tied to a room. The WebSocket framing, y-protocols sync/awareness messages (lib0 varints) and presence bookkeeping are parsed in Go.

1. On first subscribe: load the latest snapshot (`ydoc_snapshots`) and later updates (`ydoc_updates`). If the file has no document yet and the caller can edit, the server creates it from the page's current content in the revision (`yFromMarkdown`) and stores the source map on `ydocs`. Clients never seed documents: two browsers seeding concurrently would duplicate the page. Read-only callers can't create one; they see published content until someone edits.
2. Opening a page doesn't add it to the revision. The **first update** does (manifest entry `modify`).
3. On client update: reject when read-only or over 1 MB, validate it and read its Yjs client ids (`yClients`, which also maps client ids to users in `ydoc_clients` and refuses ids owned by someone else), append to the log (batched every 50 ms), broadcast to the other peers.
4. **Snapshot compaction**: every 500 updates, write a merged snapshot and delete the updates it covers (older snapshots are kept only when a checkpoint references them).
5. **Materialization**: after 2 s without updates (and when the last peer leaves), serialize to markdown with the source map and store `revision_files.content_md`, its hash and +/− counts. This feeds diffs, the link checker, search, the assistant, and publish without touching the live room.
6. Eviction from memory after 5 min with no subscribers.
7. Edits made outside an editor (assistant, updates from Published, checkpoint restore) go through `yApplyMarkdown`: a block-level diff by content hash that leaves unchanged blocks alone and patches same-shaped text blocks in place, so concurrent edits and comment anchors elsewhere survive.

## Presence (awareness)

Awareness state per client: `{user: {id, name, color, avatar}, cursor: {anchor, head} (relative positions), mode: "wysiwyg"|"source", viewing: true}`.

- Colors are assigned per user from a 12-color palette, stable per user.
- Cursors show a name flag for 3 s after movement, then shrink to a caret.
- Top-bar avatar stack shows everyone in the revision (any file), highlighted if in the same file. Clicking an avatar jumps to their cursor ("follow" mode toggles on double-click).
- Presence of users on the published view (no revision) is shown too, read-only.

## Suggestions (tracked changes)

Suggesting mode is a per-user toggle. When on, the editor rewrites transactions before they apply:

- Inserted text gets an `insertion` mark `{id, author, at}`; nodes that start inside an insertion (a split paragraph, a pasted block) get a `suggestion: {kind: "insert"}` attribute.
- Deleted text is not removed; it gets a `deletion` mark and is rendered struck through. A deletion across a block boundary marks the next block `join`; blocks deleted whole are marked `delete`. Deleting your own pending insertion removes it for real.
- Replacements produce a deletion and an insertion with the same id; typing next to your own suggestion extends it.
- Node-level changes (paragraph → heading, a task checked, a math or front matter edit) are captured as `suggestion: {kind: "change", from: {type, attrs}}`. A pending join or deletion can carry a `from` too.
- v1 refuses what it can't record yet: formatting-mark changes and structural steps (wrapping in a list or quote, table structure). The editor says so and suggests turning Suggesting off.

The marks and attributes are the whole record: author and time live in the CRDT with the suggestion, and the server lists pending suggestions from the document (`GET /revisions/{id}/suggestions`). There is no suggestions table in v1; accepting or rejecting records a `suggestions_accepted` / `suggestions_rejected` revision event. Threads on suggestions come later.

**Accept** removes the marks (insertion kept, deletion text removed, joins joined). **Reject** does the opposite. Both run on the server (`POST /revisions/{id}/suggestions/resolve`, by id, by author, or all), so editors can resolve while they're read-only In review. Accepted text keeps its Yjs items (marks change in place), so it's still attributed to the suggestion's author. Accept/reject is allowed for revision editors and assigned reviewers (editors keep it while In review) and for a suggestion's own author. Bulk "Accept all from <author>" exists.

The materialized markdown used for diff and publish treats pending suggestions as **not applied**: the serializer rejects them all first (insertions excluded, deletions kept, splits joined back). Approving and publishing are blocked while suggestions are pending. Source mode is read-only on a page with pending suggestions. Edits made outside the editor (link updates, the assistant) diff against the settled page, so blocks they don't touch keep their suggestions.

## Saving and checkpoints

Live edits sync continuously (the editor shows "Synced"), but a revision's history is its commits ([D59](decisions.md)):

- **Save all** (editor top bar, where Done was) flushes the live rooms and writes **one commit** on the revision's branch with the revision's current content, then pushes it to the pull request ([06](06-git-and-forges.md#revision-branches)). The commit is authored by the person who clicked (their commit email), committed by kmdn, and titled after the changed pages ("Update onboarding.md and faq.md") unless they typed a message in the Checkpoints panel. When a second person clicks Save all, that's another commit, authored by them. Nothing changed since the last save → "All changes saved", no commit.
- **Checkpoints are those commits**: each save records a checkpoint with its commit SHA and the content it saved (per file: Yjs snapshot reference and materialized markdown), listed in the Checkpoints panel with a link to the commit on the forge. There are no automatic checkpoints.
- Actions that would otherwise lose track of unsaved work save first, as a commit by the person acting: **Submit for review**, **applying updates from Published**, **restoring a checkpoint** and **publishing**.
- The revision shows **Unsaved changes** when its content differs from the last commit (the content hash of manifest, materialized pages and assets).
- **Restore** brings back a checkpoint's content as new changes (never rewinds the CRDT or git), which the next Save all commits.

## Contribution tracking (for attribution)

Yjs client IDs are mapped to users per connection (`ydoc_clients(client_id, user_id)`). At publish, the engine walks the final Y.XmlFragment items and attributes surviving inserted content to client IDs → users. Accepted suggestions attribute to the suggestion author. Assistant edits carry a dedicated client ID mapped to the requesting user plus an `assisted` flag. See [06](06-git-and-forges.md#attribution).

## Limits

| Limit | Default |
|-------|---------|
| Max file size editable in WYSIWYG | 1 MB (larger opens source-only) |
| Max concurrent editors per file | 50 |
| Max update size | 1 MB |
| Max files per revision | 200 |
| Disconnected edit buffer | 60 s, then read-only |
