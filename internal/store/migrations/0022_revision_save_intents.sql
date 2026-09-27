-- A prepared save survives failures between Git push and checkpoint commit.
CREATE TABLE revision_save_intents (
    revision_id TEXT PRIMARY KEY REFERENCES revisions (id) ON DELETE CASCADE,
    checkpoint_id TEXT NOT NULL,
    branch TEXT NOT NULL,
    commit_sha TEXT NOT NULL,
    prior_branch_sha TEXT NOT NULL,
    base_sha TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    created_by TEXT REFERENCES users (id) ON DELETE SET NULL,
    created_at BIGINT NOT NULL,
    name TEXT NOT NULL DEFAULT '',
    commit_objects BLOB NOT NULL,
    payload BLOB NOT NULL
);

-- New saved snapshots belong to the checkpoint, independent of live ydocs.
-- Older checkpoints retain their ydoc_snapshot_id references.
ALTER TABLE revision_checkpoint_files ADD COLUMN ydoc_state BLOB;
