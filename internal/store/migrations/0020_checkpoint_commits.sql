-- Checkpoints are the commits Save all writes on the revision's branch
-- (docs/specs/05-collaboration.md#saving-and-checkpoints). commit_sha is
-- empty for checkpoints taken before that (kinds auto, named, submit, …).
ALTER TABLE revision_checkpoints ADD COLUMN commit_sha TEXT NOT NULL DEFAULT '';
-- content_hash: revisions.ContentHash when saved, to tell unsaved changes.
ALTER TABLE revision_checkpoints ADD COLUMN content_hash TEXT NOT NULL DEFAULT '';
