-- Link index for the published content (backlinks, broken links, rename rewriting).

CREATE TABLE link_sources (
    repo_id TEXT NOT NULL REFERENCES repos (id) ON DELETE CASCADE,
    path TEXT NOT NULL,
    blob_sha TEXT NOT NULL,
    headings TEXT NOT NULL DEFAULT '[]',
    PRIMARY KEY (repo_id, path)
);

CREATE TABLE links (
    repo_id TEXT NOT NULL REFERENCES repos (id) ON DELETE CASCADE,
    from_path TEXT NOT NULL,
    url TEXT NOT NULL,
    to_path TEXT NOT NULL DEFAULT '',
    anchor TEXT NOT NULL DEFAULT '',
    kind TEXT NOT NULL,
    line INTEGER NOT NULL
);
CREATE INDEX links_from ON links (repo_id, from_path);
CREATE INDEX links_to ON links (repo_id, to_path);
