-- Assistant runs: who used the model, for what, and how many tokens
-- (budgets and the admin usage view; docs/specs/08-assistant.md#limits-cost-and-privacy).

CREATE TABLE assistant_runs (
    id TEXT PRIMARY KEY,
    user_id TEXT REFERENCES users (id) ON DELETE SET NULL,
    repo_id TEXT REFERENCES repos (id) ON DELETE CASCADE,
    revision_id TEXT REFERENCES revisions (id) ON DELETE SET NULL,
    -- chat | review_summary | short_text | capability_check
    task TEXT NOT NULL,
    provider TEXT NOT NULL,
    model TEXT NOT NULL,
    input_tokens INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens INTEGER NOT NULL DEFAULT 0,
    cache_write_tokens INTEGER NOT NULL DEFAULT 0,
    tools TEXT NOT NULL DEFAULT '[]',
    -- running | done | error | stopped
    status TEXT NOT NULL,
    error TEXT NOT NULL DEFAULT '',
    started_at BIGINT NOT NULL,
    finished_at BIGINT
);

CREATE INDEX assistant_runs_user ON assistant_runs (user_id, started_at);
CREATE INDEX assistant_runs_started ON assistant_runs (started_at);
