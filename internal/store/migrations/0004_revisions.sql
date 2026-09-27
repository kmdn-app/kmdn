-- Revisions, their file manifest, and collaborative document storage (M2).

CREATE TABLE revisions (
    id TEXT PRIMARY KEY,
    repo_id TEXT NOT NULL REFERENCES repos (id) ON DELETE CASCADE,
    number INTEGER NOT NULL,
    title TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL DEFAULT 'editing',
    changes_requested BOOLEAN NOT NULL DEFAULT FALSE,
    has_conflicts BOOLEAN NOT NULL DEFAULT FALSE,
    review_round INTEGER NOT NULL DEFAULT 0,
    base_sha TEXT NOT NULL,
    created_by TEXT REFERENCES users (id) ON DELETE SET NULL,
    created_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL,
    submitted_at BIGINT,
    published_at BIGINT,
    published_sha TEXT NOT NULL DEFAULT '',
    change_request_url TEXT NOT NULL DEFAULT '',
    change_request_ref TEXT NOT NULL DEFAULT '',
    closed_at BIGINT,
    archived_at BIGINT
);
CREATE UNIQUE INDEX revisions_number ON revisions (repo_id, number);
CREATE INDEX revisions_state ON revisions (repo_id, state, updated_at);

CREATE TABLE revision_members (
    revision_id TEXT NOT NULL REFERENCES revisions (id) ON DELETE CASCADE,
    user_id TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role TEXT NOT NULL,
    invited_by TEXT REFERENCES users (id) ON DELETE SET NULL,
    added_at BIGINT NOT NULL,
    PRIMARY KEY (revision_id, user_id)
);
CREATE INDEX revision_members_user ON revision_members (user_id);

-- The manifest: one row per path the revision touches. A rename is a single
-- row at the new path with from_path set.
CREATE TABLE revision_files (
    id TEXT PRIMARY KEY,
    revision_id TEXT NOT NULL REFERENCES revisions (id) ON DELETE CASCADE,
    path TEXT NOT NULL,
    op TEXT NOT NULL,
    from_path TEXT NOT NULL DEFAULT '',
    base_md TEXT NOT NULL DEFAULT '',
    base_blob_sha TEXT NOT NULL DEFAULT '',
    content_md TEXT NOT NULL DEFAULT '',
    content_hash TEXT NOT NULL DEFAULT '',
    materialized_at BIGINT,
    has_conflicts BOOLEAN NOT NULL DEFAULT FALSE,
    additions INTEGER NOT NULL DEFAULT 0,
    deletions INTEGER NOT NULL DEFAULT 0,
    ydoc_id TEXT,
    created_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL
);
CREATE UNIQUE INDEX revision_files_path ON revision_files (revision_id, path);

CREATE TABLE revision_events (
    id TEXT PRIMARY KEY,
    revision_id TEXT NOT NULL REFERENCES revisions (id) ON DELETE CASCADE,
    actor_type TEXT NOT NULL,
    actor_id TEXT NOT NULL DEFAULT '',
    kind TEXT NOT NULL,
    data TEXT NOT NULL DEFAULT '{}',
    created_at BIGINT NOT NULL
);
CREATE INDEX revision_events_rev ON revision_events (revision_id, created_at);

CREATE TABLE revision_checkpoints (
    id TEXT PRIMARY KEY,
    revision_id TEXT NOT NULL REFERENCES revisions (id) ON DELETE CASCADE,
    name TEXT NOT NULL DEFAULT '',
    kind TEXT NOT NULL,
    created_by TEXT REFERENCES users (id) ON DELETE SET NULL,
    created_at BIGINT NOT NULL
);
CREATE INDEX revision_checkpoints_rev ON revision_checkpoints (revision_id, created_at);

CREATE TABLE revision_checkpoint_files (
    checkpoint_id TEXT NOT NULL REFERENCES revision_checkpoints (id) ON DELETE CASCADE,
    path TEXT NOT NULL,
    op TEXT NOT NULL,
    from_path TEXT NOT NULL DEFAULT '',
    ydoc_snapshot_id TEXT,
    content_md TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (checkpoint_id, path)
);

-- One Y.Doc per revision file (docs/specs/05-collaboration.md).
CREATE TABLE ydocs (
    id TEXT PRIMARY KEY,
    revision_id TEXT NOT NULL REFERENCES revisions (id) ON DELETE CASCADE,
    path TEXT NOT NULL,
    engine_version INTEGER NOT NULL,
    source_map TEXT NOT NULL DEFAULT '',
    created_at BIGINT NOT NULL
);
CREATE UNIQUE INDEX ydocs_path ON ydocs (revision_id, path);

CREATE TABLE ydoc_snapshots (
    id TEXT PRIMARY KEY,
    ydoc_id TEXT NOT NULL REFERENCES ydocs (id) ON DELETE CASCADE,
    state BLOB NOT NULL,
    update_seq BIGINT NOT NULL,
    created_at BIGINT NOT NULL
);
CREATE INDEX ydoc_snapshots_doc ON ydoc_snapshots (ydoc_id, update_seq);

CREATE TABLE ydoc_updates (
    ydoc_id TEXT NOT NULL REFERENCES ydocs (id) ON DELETE CASCADE,
    seq BIGINT NOT NULL,
    data BLOB NOT NULL,
    user_id TEXT NOT NULL DEFAULT '',
    created_at BIGINT NOT NULL,
    PRIMARY KEY (ydoc_id, seq)
);

CREATE TABLE ydoc_clients (
    ydoc_id TEXT NOT NULL REFERENCES ydocs (id) ON DELETE CASCADE,
    client_id BIGINT NOT NULL,
    user_id TEXT NOT NULL,
    kind TEXT NOT NULL DEFAULT 'human',
    on_behalf_of TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (ydoc_id, client_id)
);
