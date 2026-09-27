-- Why an org is suspended (read-only), shown to its members.
ALTER TABLE orgs ADD COLUMN status_reason TEXT NOT NULL DEFAULT '';
