-- The review assistant's summary card and suggested commit per revision,
-- and one-line change summaries per published commit and page
-- (docs/specs/07-review.md#review-assistant, 13-operations.md#readers-of-published-pages).

CREATE TABLE review_summaries (
    revision_id TEXT PRIMARY KEY REFERENCES revisions (id) ON DELETE CASCADE,
    -- the content it was written for (stale when the revision changed since)
    content_hash TEXT NOT NULL,
    summary TEXT NOT NULL DEFAULT '',
    commit_title TEXT NOT NULL DEFAULT '',
    commit_body TEXT NOT NULL DEFAULT '',
    findings TEXT NOT NULL DEFAULT '[]',
    model TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    created_at BIGINT NOT NULL
);

CREATE TABLE change_summaries (
    repo_id TEXT NOT NULL REFERENCES repos (id) ON DELETE CASCADE,
    commit_sha TEXT NOT NULL,
    path TEXT NOT NULL,
    summary TEXT NOT NULL,
    created_at BIGINT NOT NULL,
    PRIMARY KEY (repo_id, commit_sha, path)
);
