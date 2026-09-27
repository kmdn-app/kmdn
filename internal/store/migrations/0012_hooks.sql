-- Outgoing webhooks and Slack messages per repository (docs/specs/11-api.md#outgoing-webhooks).

CREATE TABLE repo_hooks (
    id TEXT PRIMARY KEY,
    repo_id TEXT NOT NULL REFERENCES repos (id) ON DELETE CASCADE,
    -- generic | slack
    kind TEXT NOT NULL,
    -- The URL can carry a token (Slack): kept in the secrets store; the host is shown.
    url_ref TEXT NOT NULL,
    url_host TEXT NOT NULL,
    secret_ref TEXT NOT NULL DEFAULT '',
    events TEXT NOT NULL DEFAULT '[]',
    active BOOLEAN NOT NULL DEFAULT TRUE,
    created_by TEXT REFERENCES users (id) ON DELETE SET NULL,
    created_at BIGINT NOT NULL
);

CREATE INDEX repo_hooks_repo ON repo_hooks (repo_id);

CREATE TABLE hook_deliveries (
    id TEXT PRIMARY KEY,
    hook_id TEXT NOT NULL REFERENCES repo_hooks (id) ON DELETE CASCADE,
    event_type TEXT NOT NULL,
    payload TEXT NOT NULL,
    -- pending | delivered | failed
    status TEXT NOT NULL DEFAULT 'pending',
    attempts INTEGER NOT NULL DEFAULT 0,
    response_code INTEGER NOT NULL DEFAULT 0,
    response_body TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    created_at BIGINT NOT NULL,
    last_attempt_at BIGINT,
    delivered_at BIGINT
);

CREATE INDEX hook_deliveries_hook ON hook_deliveries (hook_id, created_at);
