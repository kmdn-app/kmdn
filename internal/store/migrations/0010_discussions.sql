-- Discussions on published pages (docs/specs/07-review.md#doc-discussions-published-docs):
-- a quote that no longer matches after a publish is outdated; "Fix this"
-- links the discussion to the revision fixing it.

ALTER TABLE threads ADD COLUMN outdated BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE threads ADD COLUMN fix_revision_id TEXT REFERENCES revisions (id) ON DELETE SET NULL;

CREATE INDEX threads_repo_kind ON threads (repo_id, kind, state);
