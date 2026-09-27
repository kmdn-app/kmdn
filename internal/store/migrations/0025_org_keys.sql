-- Per-org keys for org secrets (docs/specs/16-organizations.md#secrets):
-- 32 random bytes wrapped by the instance key. Deleting a row makes the
-- org's secrets unreadable.
CREATE TABLE org_keys (
    org_id TEXT PRIMARY KEY REFERENCES orgs (id) ON DELETE CASCADE,
    wrapped_key BLOB NOT NULL,
    kek_version INTEGER NOT NULL DEFAULT 1,
    created_at BIGINT NOT NULL,
    rotated_at BIGINT
);
CREATE INDEX secrets_org ON secrets (org_id);
