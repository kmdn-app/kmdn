-- Review: assigned reviewers and their approvals (docs/specs/07-review.md).

CREATE TABLE revision_reviewers (
    revision_id TEXT NOT NULL REFERENCES revisions (id) ON DELETE CASCADE,
    user_id TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    requested_by TEXT REFERENCES users (id) ON DELETE SET NULL,
    created_at BIGINT NOT NULL,
    removed_at BIGINT,
    PRIMARY KEY (revision_id, user_id)
);
CREATE INDEX revision_reviewers_user ON revision_reviewers (user_id);

CREATE TABLE approvals (
    id TEXT PRIMARY KEY,
    revision_id TEXT NOT NULL REFERENCES revisions (id) ON DELETE CASCADE,
    user_id TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    review_round INTEGER NOT NULL,
    state TEXT NOT NULL,
    content_hash TEXT NOT NULL DEFAULT '',
    note TEXT NOT NULL DEFAULT '',
    created_at BIGINT NOT NULL,
    dismissed_at BIGINT
);
CREATE INDEX approvals_rev ON approvals (revision_id, review_round);
