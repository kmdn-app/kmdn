-- Organizations (docs/specs/16-organizations.md). Every existing row joins
-- the default org, org_default; instance admins become its owners.

CREATE TABLE orgs (
    id TEXT PRIMARY KEY,
    slug TEXT NOT NULL,
    name TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'active',
    created_at BIGINT NOT NULL,
    deleted_at BIGINT
);
CREATE UNIQUE INDEX orgs_slug ON orgs (slug);

CREATE TABLE org_members (
    org_id TEXT NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    user_id TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role TEXT NOT NULL DEFAULT 'member',
    status TEXT NOT NULL DEFAULT 'active',
    invited_by TEXT REFERENCES users (id) ON DELETE SET NULL,
    joined_at BIGINT NOT NULL,
    PRIMARY KEY (org_id, user_id)
);
CREATE INDEX org_members_user ON org_members (user_id);

CREATE TABLE org_settings (
    org_id TEXT NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    key TEXT NOT NULL,
    value TEXT NOT NULL,
    updated_at BIGINT NOT NULL,
    PRIMARY KEY (org_id, key)
);

CREATE TABLE org_domains (
    domain TEXT PRIMARY KEY,
    org_id TEXT NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    verified_at BIGINT,
    created_at BIGINT NOT NULL
);
CREATE INDEX org_domains_org ON org_domains (org_id);

-- The default org takes the instance name when setup already stored one.
-- +sqlite
INSERT INTO orgs (id, slug, name, created_at)
SELECT 'org_default', 'default',
       COALESCE((SELECT trim(value, '"') FROM instance_settings WHERE key = 'instance_name'), 'Default'),
       CAST(strftime('%s', 'now') AS BIGINT) * 1000;
-- +postgres
INSERT INTO orgs (id, slug, name, created_at)
SELECT 'org_default', 'default',
       COALESCE((SELECT trim(both '"' from value) FROM instance_settings WHERE key = 'instance_name'), 'Default'),
       CAST(EXTRACT(EPOCH FROM now()) * 1000 AS BIGINT);
-- +end

INSERT INTO org_members (org_id, user_id, role, status, joined_at)
SELECT 'org_default', id, CASE WHEN is_instance_admin THEN 'owner' ELSE 'member' END, status, created_at FROM users;

-- Root tables (16-organizations.md#data-model). Rows under a repo or a
-- revision belong to the repo's org and don't carry org_id.
ALTER TABLE repos ADD COLUMN org_id TEXT NOT NULL DEFAULT 'org_default';
ALTER TABLE forge_installs ADD COLUMN org_id TEXT NOT NULL DEFAULT 'org_default';
ALTER TABLE groups ADD COLUMN org_id TEXT NOT NULL DEFAULT 'org_default';
ALTER TABLE invites ADD COLUMN org_id TEXT NOT NULL DEFAULT 'org_default';
ALTER TABLE agent_keys ADD COLUMN org_id TEXT NOT NULL DEFAULT 'org_default';
ALTER TABLE uploads ADD COLUMN org_id TEXT NOT NULL DEFAULT 'org_default';
ALTER TABLE assistant_threads ADD COLUMN org_id TEXT NOT NULL DEFAULT 'org_default';
ALTER TABLE notifications ADD COLUMN org_id TEXT NOT NULL DEFAULT 'org_default';
-- NULL: shared by the instance (forge hosts, secrets, jobs, audit events,
-- AI capability checks) or not yet routed (webhook deliveries).
ALTER TABLE forge_hosts ADD COLUMN org_id TEXT;
ALTER TABLE secrets ADD COLUMN org_id TEXT;
ALTER TABLE jobs ADD COLUMN org_id TEXT;
ALTER TABLE assistant_runs ADD COLUMN org_id TEXT;
ALTER TABLE audit_log ADD COLUMN org_id TEXT;
ALTER TABLE webhook_deliveries ADD COLUMN org_id TEXT;

-- Existing forge hosts were set up by this instance's admins for its only
-- org; audit events and jobs about a repo belong to its org.
UPDATE forge_hosts SET org_id = 'org_default';
UPDATE audit_log SET org_id = 'org_default' WHERE repo_id IS NOT NULL OR actor_type <> 'system';
UPDATE webhook_deliveries SET org_id = 'org_default' WHERE repo_id IS NOT NULL;
UPDATE assistant_runs SET org_id = 'org_default' WHERE task <> 'capability_check';

CREATE INDEX repos_org ON repos (org_id);
CREATE INDEX forge_hosts_org ON forge_hosts (org_id);
CREATE INDEX forge_installs_org ON forge_installs (org_id);
CREATE INDEX invites_org ON invites (org_id);
CREATE INDEX agent_keys_org ON agent_keys (org_id);
CREATE INDEX assistant_runs_org ON assistant_runs (org_id, started_at);
CREATE INDEX assistant_threads_org ON assistant_threads (org_id);
CREATE INDEX notifications_org_user ON notifications (org_id, user_id);
CREATE INDEX jobs_org ON jobs (org_id, status);
CREATE INDEX audit_log_org ON audit_log (org_id, at);

-- Names and slugs are unique per org; uploads are never shared across orgs.
DROP INDEX groups_name;
CREATE UNIQUE INDEX groups_name ON groups (org_id, name);
DROP INDEX repos_slug;
CREATE UNIQUE INDEX repos_slug ON repos (org_id, forge_host_id, owner, name);
DROP INDEX uploads_sha;
CREATE UNIQUE INDEX uploads_sha ON uploads (org_id, sha256);
