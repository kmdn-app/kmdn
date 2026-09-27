-- Forges (docs/specs/16-organizations.md#forges). Hosts set up in the
-- instance console are shared by every org (org_id NULL); an org can add its
-- own. A GitHub installation is claimed by one org.
UPDATE forge_hosts SET org_id = NULL;

CREATE TABLE install_claims (
    install_id TEXT PRIMARY KEY REFERENCES forge_installs (id) ON DELETE CASCADE,
    org_id TEXT NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    claimed_by TEXT REFERENCES users (id) ON DELETE SET NULL,
    claimed_at BIGINT NOT NULL
);
CREATE INDEX install_claims_org ON install_claims (org_id);

-- Installations known before belong to the only org there was.
INSERT INTO install_claims (install_id, org_id, claimed_at) SELECT id, org_id, created_at FROM forge_installs;

DROP INDEX forge_installs_org;
ALTER TABLE forge_installs DROP COLUMN org_id;
