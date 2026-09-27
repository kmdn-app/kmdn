-- Uploaded files (content-addressed on disk) and the assets a revision adds.

CREATE TABLE uploads (
    id TEXT PRIMARY KEY,
    sha256 TEXT NOT NULL,
    size BIGINT NOT NULL,
    mime TEXT NOT NULL,
    created_by TEXT REFERENCES users (id) ON DELETE SET NULL,
    created_at BIGINT NOT NULL
);
CREATE UNIQUE INDEX uploads_sha ON uploads (sha256);

CREATE TABLE revision_assets (
    id TEXT PRIMARY KEY,
    revision_id TEXT NOT NULL REFERENCES revisions (id) ON DELETE CASCADE,
    path TEXT NOT NULL,
    upload_id TEXT NOT NULL REFERENCES uploads (id),
    size BIGINT NOT NULL,
    mime TEXT NOT NULL,
    sha256 TEXT NOT NULL,
    created_by TEXT REFERENCES users (id) ON DELETE SET NULL,
    created_at BIGINT NOT NULL
);
CREATE UNIQUE INDEX revision_assets_path ON revision_assets (revision_id, path);
