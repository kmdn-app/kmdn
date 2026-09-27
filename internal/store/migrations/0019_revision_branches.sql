-- Every revision is a branch on the forge with a draft pull/merge request
-- (docs/specs/06-git-and-forges.md#revision-branches).

-- branch: kmdn/<number>-<slug>. branch_sha: the tip kmdn last pushed (the
-- lease for the next push). branch_base_sha: the Published commit the branch
-- last merged; when base_sha moves past it, the next commit merges base_sha.
ALTER TABLE revisions ADD COLUMN branch TEXT NOT NULL DEFAULT '';
ALTER TABLE revisions ADD COLUMN branch_sha TEXT NOT NULL DEFAULT '';
ALTER TABLE revisions ADD COLUMN branch_base_sha TEXT NOT NULL DEFAULT '';
-- change_request_node: GitHub's GraphQL id (draft ↔ ready go through GraphQL).
ALTER TABLE revisions ADD COLUMN change_request_node TEXT NOT NULL DEFAULT '';
ALTER TABLE revisions ADD COLUMN change_request_draft BOOLEAN NOT NULL DEFAULT FALSE;
