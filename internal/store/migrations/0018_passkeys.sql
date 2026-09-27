-- Passkeys (WebAuthn discoverable credentials), docs/specs/09-auth-permissions.md#sign-in-methods.

CREATE TABLE passkeys (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    -- base64url credential id (what the authenticator returns as rawId)
    credential_id TEXT NOT NULL UNIQUE,
    -- the go-webauthn Credential: public key, sign count, flags, transports, AAGUID
    credential TEXT NOT NULL,
    name TEXT NOT NULL,
    created_at BIGINT NOT NULL,
    last_used_at BIGINT
);

CREATE INDEX passkeys_user ON passkeys (user_id);
