-- Every insert names its org now: on Postgres, forgetting it is an error
-- instead of a row in the default org. (SQLite can't drop a column default;
-- the cross-org tests guard it there.)
-- +postgres
ALTER TABLE repos ALTER COLUMN org_id DROP DEFAULT;
ALTER TABLE groups ALTER COLUMN org_id DROP DEFAULT;
ALTER TABLE invites ALTER COLUMN org_id DROP DEFAULT;
ALTER TABLE agent_keys ALTER COLUMN org_id DROP DEFAULT;
ALTER TABLE uploads ALTER COLUMN org_id DROP DEFAULT;
ALTER TABLE assistant_threads ALTER COLUMN org_id DROP DEFAULT;
ALTER TABLE notifications ALTER COLUMN org_id DROP DEFAULT;
-- +end
