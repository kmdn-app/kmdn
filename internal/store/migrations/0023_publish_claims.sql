-- A claim freezes the approved snapshot before any merge side effect.
CREATE TABLE revision_publish_claims (
    revision_id TEXT PRIMARY KEY REFERENCES revisions(id) ON DELETE CASCADE,
    id TEXT NOT NULL UNIQUE,
    branch_sha TEXT NOT NULL,
    branch_base_sha TEXT NOT NULL,
    base_sha TEXT NOT NULL,
    target_branch TEXT NOT NULL,
    target_sha TEXT NOT NULL,
    merge_sha TEXT NOT NULL DEFAULT '',
    merge_objects BLOB NOT NULL,
    message TEXT NOT NULL,
    actor_id TEXT NOT NULL,
    phase TEXT NOT NULL DEFAULT 'claimed',
    created_at BIGINT NOT NULL
);
