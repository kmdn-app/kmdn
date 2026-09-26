-- Forges, repositories, groups and access (M1).

CREATE TABLE forge_hosts (
    id TEXT PRIMARY KEY,
    kind TEXT NOT NULL,
    base_url TEXT NOT NULL DEFAULT '',
    api_url TEXT NOT NULL DEFAULT '',
    display_name TEXT NOT NULL,
    app_id TEXT NOT NULL DEFAULT '',
    app_slug TEXT NOT NULL DEFAULT '',
    client_id TEXT NOT NULL DEFAULT '',
    client_secret_ref TEXT,
    private_key_ref TEXT,
    webhook_secret_ref TEXT,
    created_at BIGINT NOT NULL
);

CREATE TABLE forge_installs (
    id TEXT PRIMARY KEY,
    forge_host_id TEXT NOT NULL REFERENCES forge_hosts (id) ON DELETE CASCADE,
    external_id TEXT NOT NULL,
    account_login TEXT NOT NULL,
    created_at BIGINT NOT NULL
);
CREATE UNIQUE INDEX forge_installs_ext ON forge_installs (forge_host_id, external_id);

CREATE TABLE repos (
    id TEXT PRIMARY KEY,
    forge_host_id TEXT NOT NULL REFERENCES forge_hosts (id),
    install_id TEXT REFERENCES forge_installs (id) ON DELETE SET NULL,
    external_id TEXT NOT NULL DEFAULT '',
    owner TEXT NOT NULL,
    name TEXT NOT NULL,
    display_name TEXT NOT NULL,
    clone_url TEXT NOT NULL DEFAULT '',
    web_url TEXT NOT NULL DEFAULT '',
    default_branch TEXT NOT NULL DEFAULT '',
    target_branch TEXT NOT NULL,
    content_root TEXT NOT NULL DEFAULT '',
    include_globs TEXT NOT NULL DEFAULT '[]',
    exclude_globs TEXT NOT NULL DEFAULT '[]',
    settings TEXT NOT NULL DEFAULT '{}',
    token_ref TEXT,
    webhook_secret_ref TEXT,
    protection TEXT NOT NULL DEFAULT '{}',
    protection_checked_at BIGINT,
    head_sha TEXT NOT NULL DEFAULT '',
    health TEXT NOT NULL DEFAULT 'pending',
    health_detail TEXT NOT NULL DEFAULT '',
    kmdn_yml TEXT NOT NULL DEFAULT '{}',
    last_fetch_at BIGINT,
    created_by TEXT REFERENCES users (id) ON DELETE SET NULL,
    created_at BIGINT NOT NULL
);
CREATE UNIQUE INDEX repos_slug ON repos (forge_host_id, owner, name);
CREATE INDEX repos_external ON repos (forge_host_id, external_id);

CREATE TABLE groups (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    created_at BIGINT NOT NULL
);
CREATE UNIQUE INDEX groups_name ON groups (name);

CREATE TABLE group_members (
    group_id TEXT NOT NULL REFERENCES groups (id) ON DELETE CASCADE,
    user_id TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    added_at BIGINT NOT NULL,
    PRIMARY KEY (group_id, user_id)
);
CREATE INDEX group_members_user ON group_members (user_id);

CREATE TABLE repo_members (
    repo_id TEXT NOT NULL REFERENCES repos (id) ON DELETE CASCADE,
    principal_type TEXT NOT NULL,
    principal_id TEXT NOT NULL,
    role TEXT NOT NULL,
    added_at BIGINT NOT NULL,
    PRIMARY KEY (repo_id, principal_type, principal_id)
);
CREATE INDEX repo_members_principal ON repo_members (principal_type, principal_id);

CREATE TABLE linked_accounts (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    forge_host_id TEXT NOT NULL REFERENCES forge_hosts (id) ON DELETE CASCADE,
    forge_user_id TEXT NOT NULL,
    login TEXT NOT NULL,
    emails TEXT NOT NULL DEFAULT '[]',
    noreply_email TEXT NOT NULL DEFAULT '',
    avatar_url TEXT NOT NULL DEFAULT '',
    linked_at BIGINT NOT NULL
);
CREATE UNIQUE INDEX linked_accounts_forge_user ON linked_accounts (forge_host_id, forge_user_id);
CREATE INDEX linked_accounts_user ON linked_accounts (user_id);

CREATE TABLE webhook_deliveries (
    id TEXT PRIMARY KEY,
    forge_host_id TEXT,
    repo_id TEXT,
    event TEXT NOT NULL,
    received_at BIGINT NOT NULL,
    processed_at BIGINT,
    error TEXT
);
CREATE INDEX webhook_deliveries_received ON webhook_deliveries (received_at);
