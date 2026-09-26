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

1. On first subscribe: load latest snapshot (`ydoc_snapshots`) + subsequent updates (`ydoc_updates`) into a JS runtime, build state.
2. On client update: validate size (≤ 1 MB per update), apply in the engine, append to `ydoc_updates` (single INSERT, batched per 50 ms), broadcast to other subscribers, record `(user_id, bytes, timestamp)` into contribution tracking.
3. **Snapshot compaction**: every 500 updates or 5 minutes of inactivity, write a full state snapshot and delete updates older than the snapshot (keeping them if a checkpoint references them; see below).
4. **Materialization**: after each quiet period (2 s), serialize to markdown and store `revision_files.content_md` + hash. This feeds diffs, the link checker, search, the assistant, and publish without touching the live room.
5. Eviction after 5 min with no subscribers.

## Presence (awareness)

Awareness state per client: `{user: {id, name, color, avatar}, cursor: {anchor, head} (relative positions), mode: "wysiwyg"|"source", viewing: true}`.

- Colors are assigned per user from a 12-color palette, stable per user.
- Cursors show a name flag for 3 s after movement, then shrink to a caret.
- Top-bar avatar stack shows everyone in the revision (any file), highlighted if in the same file. Clicking an avatar jumps to their cursor ("follow" mode toggles on double-click).
- Presence of users on the published view (no revision) is shown too, read-only.

## Suggestions (tracked changes)

Suggesting mode is a per-user toggle. When on, the editor wraps transactions:

- Inserted text gets an `insertion` mark `{suggestionId, authorId, createdAt}`.
- Deleted text is not removed; it gets a `deletion` mark and is rendered struck through.
- Replacements produce an adjacent deletion + insertion with the same `suggestionId`.
- Node-level changes (e.g. converting a paragraph to a heading, table structure) are captured as a `nodeSuggestion` attribute holding the previous node attrs/type.

Suggestion metadata (author, status, linked thread) is in the DB `suggestions` table keyed by `suggestionId`; the marks in the CRDT carry just the id.

**Accept** removes the marks (insertion kept, deletion text removed). **Reject** does the opposite. Accept/reject is allowed for the suggestion author, revision editors and assigned reviewers (and maintainers while Editing). While In review, editors can still accept or reject suggestions reviewers made. Bulk "Accept all from <author>" exists.

The materialized markdown used for diff and publish treats pending suggestions as **not applied**: insertions excluded, deletions kept. Publishing is blocked while suggestions are pending ("3 suggestions pending · Review them").

## Checkpoints (history inside a revision)

- **Automatic checkpoints**: after 10 minutes of activity in a revision, on submit for review, before applying updates from Published, before a restore.
- **Named checkpoints**: user action "Name this checkpoint".
- A checkpoint stores, per file in the manifest: Yjs state vector + snapshot reference, and the materialized markdown. Old checkpoints are browsable rendered with per-author coloring (computed from Yjs item client IDs → user mapping stored per connection).
- **Restore** applies the inverse diff as a new change (never rewinds the CRDT), so collaborators' clients stay consistent.

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
