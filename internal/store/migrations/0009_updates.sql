-- Updates from Published: prepared merges waiting to be applied
-- (docs/specs/06-git-and-forges.md#updates-from-published).

CREATE TABLE revision_updates (
    id TEXT PRIMARY KEY,
    revision_id TEXT NOT NULL REFERENCES revisions (id) ON DELETE CASCADE,
    from_sha TEXT NOT NULL,
    target_sha TEXT NOT NULL,
    state TEXT NOT NULL DEFAULT 'pending',
    prepared_at BIGINT NOT NULL,
    applied_at BIGINT,
    applied_by TEXT REFERENCES users (id) ON DELETE SET NULL
);

CREATE INDEX revision_updates_revision ON revision_updates (revision_id, state);

CREATE TABLE revision_update_files (
    update_id TEXT NOT NULL REFERENCES revision_updates (id) ON DELETE CASCADE,
    path TEXT NOT NULL,
    -- merge | deleted_upstream | deleted_both
    kind TEXT NOT NULL,
    base_md TEXT NOT NULL DEFAULT '',
    theirs_md TEXT NOT NULL DEFAULT '',
    theirs_blob_sha TEXT NOT NULL DEFAULT '',
    conflicts INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (update_id, path)
);

-- Why a page is in conflict beyond conflict blocks in its document
-- (deleted_upstream: Published deleted a page the revision edits).
ALTER TABLE revision_files ADD COLUMN conflict TEXT NOT NULL DEFAULT '';
