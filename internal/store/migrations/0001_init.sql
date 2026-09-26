-- Core instance, identity and infrastructure tables (M0).
-- Portable SQL: TEXT, BIGINT (unix ms timestamps), INTEGER, BOOLEAN, BLOB.

CREATE TABLE instance_settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at BIGINT NOT NULL
);

CREATE TABLE users (
    id TEXT PRIMARY KEY,
    email TEXT NOT NULL,
    name TEXT NOT NULL,
    avatar_upload_id TEXT,
    locale TEXT NOT NULL DEFAULT 'en',
    theme TEXT NOT NULL DEFAULT 'system',
    commit_email_mode TEXT NOT NULL DEFAULT 'forge_noreply',
    commit_email_custom TEXT,
    is_instance_admin BOOLEAN NOT NULL DEFAULT FALSE,
    status TEXT NOT NULL DEFAULT 'active',
    created_at BIGINT NOT NULL,
    last_active_at BIGINT
);
CREATE UNIQUE INDEX users_email ON users (email);

CREATE TABLE sessions (
    id_hash TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    csrf_token TEXT NOT NULL,
    created_at BIGINT NOT NULL,
    last_seen_at BIGINT NOT NULL,
    expires_at BIGINT NOT NULL,
    ip TEXT NOT NULL DEFAULT '',
    user_agent TEXT NOT NULL DEFAULT ''
);
CREATE INDEX sessions_user ON sessions (user_id);

CREATE TABLE magic_links (
    token_hash TEXT PRIMARY KEY,
    email TEXT NOT NULL,
    code_hash TEXT NOT NULL,
    nonce_hash TEXT NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    created_at BIGINT NOT NULL,
    expires_at BIGINT NOT NULL,
    used_at BIGINT,
    ip TEXT NOT NULL DEFAULT ''
);
CREATE INDEX magic_links_email ON magic_links (email, created_at);

CREATE TABLE invites (
    id TEXT PRIMARY KEY,
    email TEXT NOT NULL,
    invited_by TEXT REFERENCES users (id) ON DELETE SET NULL,
    repo_id TEXT,
    role TEXT,
    token_hash TEXT NOT NULL,
    created_at BIGINT NOT NULL,
    expires_at BIGINT NOT NULL,
    accepted_at BIGINT
);
CREATE UNIQUE INDEX invites_token ON invites (token_hash);

CREATE TABLE secrets (
    id TEXT PRIMARY KEY,
    kind TEXT NOT NULL,
    ciphertext BLOB NOT NULL,
    data_key BLOB NOT NULL,
    kek_version INTEGER NOT NULL DEFAULT 1,
    created_at BIGINT NOT NULL,
    rotated_at BIGINT
);

CREATE TABLE jobs (
    id TEXT PRIMARY KEY,
    kind TEXT NOT NULL,
    unique_key TEXT,
    payload TEXT NOT NULL DEFAULT '{}',
    status TEXT NOT NULL DEFAULT 'pending',
    run_at BIGINT NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 10,
    locked_until BIGINT,
    locked_by TEXT,
    last_error TEXT,
    result TEXT,
    created_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL
);
CREATE INDEX jobs_ready ON jobs (status, run_at);
CREATE UNIQUE INDEX jobs_unique_active ON jobs (kind, unique_key) WHERE unique_key IS NOT NULL AND status IN ('pending', 'running');

CREATE TABLE audit_log (
    id TEXT PRIMARY KEY,
    at BIGINT NOT NULL,
    actor_type TEXT NOT NULL,
    actor_id TEXT,
    ip TEXT NOT NULL DEFAULT '',
    action TEXT NOT NULL,
    target_type TEXT,
    target_id TEXT,
    repo_id TEXT,
    data TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX audit_log_at ON audit_log (at);
CREATE INDEX audit_log_actor ON audit_log (actor_id, at);
