-- Inbox, browser push, follows and page reads (docs/specs/13-operations.md#notifications).

CREATE TABLE notifications (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    kind TEXT NOT NULL,
    repo_id TEXT REFERENCES repos (id) ON DELETE CASCADE,
    revision_id TEXT REFERENCES revisions (id) ON DELETE CASCADE,
    thread_id TEXT REFERENCES threads (id) ON DELETE CASCADE,
    actor_id TEXT REFERENCES users (id) ON DELETE SET NULL,
    data TEXT NOT NULL DEFAULT '{}',
    created_at BIGINT NOT NULL,
    read_at BIGINT
);

CREATE INDEX notifications_user ON notifications (user_id, created_at);

CREATE TABLE notification_prefs (
    user_id TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    kind TEXT NOT NULL,
    in_app BOOLEAN NOT NULL DEFAULT TRUE,
    push BOOLEAN NOT NULL DEFAULT TRUE,
    PRIMARY KEY (user_id, kind)
);

CREATE TABLE push_subscriptions (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    endpoint TEXT NOT NULL UNIQUE,
    p256dh TEXT NOT NULL,
    auth TEXT NOT NULL,
    user_agent TEXT NOT NULL DEFAULT '',
    created_at BIGINT NOT NULL,
    last_used_at BIGINT
);

-- Pages (or folders, path ending in /) people follow; auto_until marks
-- automatic follows (editors of a published page, for 30 days).
CREATE TABLE follows (
    user_id TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    repo_id TEXT NOT NULL REFERENCES repos (id) ON DELETE CASCADE,
    path TEXT NOT NULL,
    auto_until BIGINT,
    created_at BIGINT NOT NULL,
    PRIMARY KEY (user_id, repo_id, path)
);

CREATE INDEX follows_repo ON follows (repo_id, path);

CREATE TABLE page_reads (
    user_id TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    repo_id TEXT NOT NULL REFERENCES repos (id) ON DELETE CASCADE,
    path TEXT NOT NULL,
    last_read_sha TEXT NOT NULL,
    last_read_at BIGINT NOT NULL,
    PRIMARY KEY (user_id, repo_id, path)
);

-- Revision events turn into notifications once; past ones never do.
ALTER TABLE revision_events ADD COLUMN notified BOOLEAN NOT NULL DEFAULT FALSE;
UPDATE revision_events SET notified = TRUE;
