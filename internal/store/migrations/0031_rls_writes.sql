-- Row-level security, writes (docs/specs/16-organizations.md#isolation): a
-- session scoped to an org still reads the rows no org owns (the instance's
-- shared forge hosts and their secrets), but writes only its own. 0030 let
-- scoped writes touch those shared rows too. Each table's single policy is
-- split: reads as before, inserts, updates and deletes for the scope's org
-- only (or anything when unscoped). (Statements end at a line ending in ";",
-- so the block is one line.)
-- +postgres
CREATE FUNCTION kmdn_org_writable(o TEXT) RETURNS BOOLEAN LANGUAGE sql STABLE AS $$ SELECT COALESCE(current_setting('app.org_id', true), '') = '' OR o = current_setting('app.org_id', true) $$;
DO $$ DECLARE t TEXT; col TEXT; BEGIN FOR t IN SELECT tablename FROM pg_policies WHERE policyname = 'kmdn_org' AND schemaname = current_schema() LOOP col := CASE WHEN t = 'orgs' THEN 'id' ELSE 'org_id' END; EXECUTE format('DROP POLICY kmdn_org ON %I', t); EXECUTE format('CREATE POLICY kmdn_org ON %I FOR SELECT USING (kmdn_org_visible(%I))', t, col); EXECUTE format('CREATE POLICY kmdn_org_insert ON %I FOR INSERT WITH CHECK (kmdn_org_writable(%I))', t, col); EXECUTE format('CREATE POLICY kmdn_org_update ON %I FOR UPDATE USING (kmdn_org_writable(%I)) WITH CHECK (kmdn_org_writable(%I))', t, col, col); EXECUTE format('CREATE POLICY kmdn_org_delete ON %I FOR DELETE USING (kmdn_org_writable(%I))', t, col); END LOOP; END $$;
-- +end
