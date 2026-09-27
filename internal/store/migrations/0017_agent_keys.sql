-- Agent keys for the read-only MCP server (docs/specs/12-mcp.md).

CREATE TABLE agent_keys (
    -- the public part of kmdn_ak_<id>_<secret>
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    -- every repo, including ones connected later
    all_repos BOOLEAN NOT NULL DEFAULT FALSE,
    -- hex SHA-256 of the secret
    secret_hash TEXT NOT NULL,
    created_by TEXT REFERENCES users (id) ON DELETE SET NULL,
    created_at BIGINT NOT NULL,
    expires_at BIGINT,
    revoked_at BIGINT,
    last_used_at BIGINT,
    last_used_ip TEXT NOT NULL DEFAULT ''
);

CREATE TABLE agent_key_repos (
    key_id TEXT NOT NULL REFERENCES agent_keys (id) ON DELETE CASCADE,
    repo_id TEXT NOT NULL REFERENCES repos (id) ON DELETE CASCADE,
    PRIMARY KEY (key_id, repo_id)
);

-- Tool calls and resource reads per key and UTC day (the admin sparkline).
CREATE TABLE agent_key_usage (
    key_id TEXT NOT NULL REFERENCES agent_keys (id) ON DELETE CASCADE,
    day TEXT NOT NULL,
    calls INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (key_id, day)
);
