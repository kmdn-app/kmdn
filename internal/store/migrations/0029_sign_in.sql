-- Sign-in extension points (docs/specs/16-organizations.md#sign-in). A
-- session remembers how it was signed in, so an org can ask for a method;
-- sessions from before count as unknown.
ALTER TABLE sessions ADD COLUMN method TEXT NOT NULL DEFAULT '';

-- External identities (an issuer and its subject) linked to a user, apart
-- from forge accounts (linked_accounts).
CREATE TABLE identity_links (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    provider TEXT NOT NULL,
    issuer TEXT NOT NULL,
    subject TEXT NOT NULL,
    email TEXT NOT NULL DEFAULT '',
    created_at BIGINT NOT NULL,
    last_used_at BIGINT NOT NULL,
    UNIQUE (issuer, subject)
);
CREATE INDEX identity_links_user ON identity_links (user_id);
