-- Comment threads on revisions (and, later, discussions on published pages).

CREATE TABLE threads (
    id TEXT PRIMARY KEY,
    repo_id TEXT NOT NULL REFERENCES repos (id) ON DELETE CASCADE,
    revision_id TEXT REFERENCES revisions (id) ON DELETE CASCADE,
    path TEXT NOT NULL,
    kind TEXT NOT NULL,
    review_round INTEGER NOT NULL DEFAULT 0,
    anchor TEXT NOT NULL DEFAULT '{}',
    state TEXT NOT NULL DEFAULT 'open',
    created_by TEXT REFERENCES users (id) ON DELETE SET NULL,
    created_at BIGINT NOT NULL,
    last_activity_at BIGINT NOT NULL,
    resolved_by TEXT REFERENCES users (id) ON DELETE SET NULL,
    resolved_at BIGINT,
    linked_revision_id TEXT REFERENCES revisions (id) ON DELETE SET NULL
);
CREATE INDEX threads_revision ON threads (revision_id, path);
CREATE INDEX threads_page ON threads (repo_id, path, kind);

CREATE TABLE comments (
    id TEXT PRIMARY KEY,
    thread_id TEXT NOT NULL REFERENCES threads (id) ON DELETE CASCADE,
    author_id TEXT REFERENCES users (id) ON DELETE SET NULL,
    body TEXT NOT NULL,
    created_at BIGINT NOT NULL,
    edited_at BIGINT,
    deleted_at BIGINT
);
CREATE INDEX comments_thread ON comments (thread_id, created_at);

CREATE TABLE reactions (
    comment_id TEXT NOT NULL REFERENCES comments (id) ON DELETE CASCADE,
    user_id TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    kind TEXT NOT NULL,
    created_at BIGINT NOT NULL,
    PRIMARY KEY (comment_id, user_id, kind)
);
