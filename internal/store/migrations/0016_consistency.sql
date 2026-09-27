-- Consistency checks: the passage index, embeddings, LLM judgments,
-- findings and repo scans (docs/specs/08-assistant.md#consistency-check-duplicates-and-contradictions).

-- Heading sections of published pages, split at ~300 tokens. Revisions'
-- passages aren't stored: they're chunked from revision_files on each check.
CREATE TABLE passages (
    repo_id TEXT NOT NULL REFERENCES repos (id) ON DELETE CASCADE,
    path TEXT NOT NULL,
    seq INTEGER NOT NULL,
    slug TEXT NOT NULL DEFAULT '',
    heading TEXT NOT NULL DEFAULT '',
    line INTEGER NOT NULL DEFAULT 0,
    text TEXT NOT NULL,
    -- page title and heading trail, embedded with the text
    context TEXT NOT NULL DEFAULT '',
    -- hash of what is embedded (context + text)
    hash TEXT NOT NULL,
    PRIMARY KEY (repo_id, path, seq)
);

CREATE INDEX passages_hash ON passages (hash);

-- The blob each indexed page was chunked from (only changed pages are re-chunked).
CREATE TABLE passage_files (
    repo_id TEXT NOT NULL REFERENCES repos (id) ON DELETE CASCADE,
    path TEXT NOT NULL,
    blob_sha TEXT NOT NULL,
    PRIMARY KEY (repo_id, path)
);

-- float32 little-endian vectors, shared by content hash (a revision's
-- unchanged passages reuse the published ones).
CREATE TABLE passage_embeddings (
    model TEXT NOT NULL,
    hash TEXT NOT NULL,
    dim INTEGER NOT NULL,
    vector BLOB NOT NULL,
    created_at BIGINT NOT NULL,
    PRIMARY KEY (model, hash)
);

-- The model's verdict on a pair of passages, so scans only judge new pairs.
CREATE TABLE consistency_judgments (
    repo_id TEXT NOT NULL REFERENCES repos (id) ON DELETE CASCADE,
    -- the two passage hashes, sorted, joined by ':'
    pair_key TEXT NOT NULL,
    -- contradiction | duplicate | related | none
    verdict TEXT NOT NULL,
    claim_a TEXT NOT NULL DEFAULT '',
    claim_b TEXT NOT NULL DEFAULT '',
    explanation TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    judged_at BIGINT NOT NULL,
    PRIMARY KEY (repo_id, pair_key)
);

-- Pairs people chose to ignore, by passage hashes (they stay ignored in
-- every revision and scan until either passage changes).
CREATE TABLE consistency_ignores (
    repo_id TEXT NOT NULL REFERENCES repos (id) ON DELETE CASCADE,
    pair_key TEXT NOT NULL,
    reason TEXT NOT NULL DEFAULT '',
    ignored_by TEXT REFERENCES users (id) ON DELETE SET NULL,
    ignored_at BIGINT NOT NULL,
    PRIMARY KEY (repo_id, pair_key)
);

CREATE TABLE consistency_findings (
    id TEXT PRIMARY KEY,
    repo_id TEXT NOT NULL REFERENCES repos (id) ON DELETE CASCADE,
    -- 'published' (the repo scan) or a revision id (that revision's check)
    scope TEXT NOT NULL,
    pair_key TEXT NOT NULL,
    -- contradiction | duplicate
    kind TEXT NOT NULL,
    -- A is the revision's passage in a revision check
    a_path TEXT NOT NULL,
    a_slug TEXT NOT NULL DEFAULT '',
    a_heading TEXT NOT NULL DEFAULT '',
    a_line INTEGER NOT NULL DEFAULT 0,
    a_text TEXT NOT NULL,
    b_path TEXT NOT NULL,
    b_slug TEXT NOT NULL DEFAULT '',
    b_heading TEXT NOT NULL DEFAULT '',
    b_line INTEGER NOT NULL DEFAULT 0,
    b_text TEXT NOT NULL,
    claim_a TEXT NOT NULL DEFAULT '',
    claim_b TEXT NOT NULL DEFAULT '',
    explanation TEXT NOT NULL DEFAULT '',
    -- cosine similarity × 1000
    similarity INTEGER NOT NULL DEFAULT 0,
    -- open | closed (ignored pairs are in consistency_ignores)
    status TEXT NOT NULL DEFAULT 'open',
    fix_revision_id TEXT REFERENCES revisions (id) ON DELETE SET NULL,
    first_seen BIGINT NOT NULL,
    last_seen BIGINT NOT NULL,
    closed_at BIGINT,
    UNIQUE (repo_id, scope, pair_key)
);

CREATE INDEX consistency_findings_scope ON consistency_findings (repo_id, scope, status);

CREATE TABLE consistency_scans (
    id TEXT PRIMARY KEY,
    repo_id TEXT NOT NULL REFERENCES repos (id) ON DELETE CASCADE,
    -- running | done | capped | error
    status TEXT NOT NULL,
    requested_by TEXT REFERENCES users (id) ON DELETE SET NULL,
    passages INTEGER NOT NULL DEFAULT 0,
    candidates INTEGER NOT NULL DEFAULT 0,
    judged INTEGER NOT NULL DEFAULT 0,
    found INTEGER NOT NULL DEFAULT 0,
    error TEXT NOT NULL DEFAULT '',
    started_at BIGINT NOT NULL,
    finished_at BIGINT
);

CREATE INDEX consistency_scans_repo ON consistency_scans (repo_id, started_at);
