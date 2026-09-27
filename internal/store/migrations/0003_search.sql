-- Full-text search over published content (and later, revision scopes).

CREATE TABLE search_docs (
    repo_id TEXT NOT NULL REFERENCES repos (id) ON DELETE CASCADE,
    scope TEXT NOT NULL,
    path TEXT NOT NULL,
    blob_sha TEXT NOT NULL DEFAULT '',
    title TEXT NOT NULL DEFAULT '',
    headings TEXT NOT NULL DEFAULT '',
    body TEXT NOT NULL DEFAULT '',
    updated_at BIGINT NOT NULL,
    PRIMARY KEY (repo_id, scope, path)
);

-- +sqlite
CREATE VIRTUAL TABLE search_fts USING fts5(repo_id UNINDEXED, scope UNINDEXED, path UNINDEXED, title, headings, body, tokenize = 'unicode61 remove_diacritics 2');
-- +end

-- +postgres
ALTER TABLE search_docs ADD COLUMN tsv tsvector GENERATED ALWAYS AS (
    setweight(to_tsvector('simple', title), 'A') || setweight(to_tsvector('simple', headings), 'B') || setweight(to_tsvector('simple', body), 'C')
) STORED;
CREATE INDEX search_docs_tsv ON search_docs USING GIN (tsv);
-- +end
