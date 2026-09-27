# 09 · Auth and permissions

## Accounts

- kmdn owns user accounts. Accounts belong to the instance and join one or more **organizations** as owners, admins or members; a self-hosted install has a single default org that every account belongs to. Org roles, org-scoped invites and auto-join, and how instance admins relate to orgs are in [16 · Organizations](16-organizations.md#roles).
- A user has: id, display name, primary email (unique, verified), avatar (uploaded or from linked forge), locale, theme, commit-email preference, status (active, deactivated), `is_instance_admin`.
- New users only through **invites** (org admin or repo admin) or **auto-join** rules: org setting "Allow sign-up for emails in domains: northwind.dev" (off by default). Auto-joined users get no repo access until granted, unless a repo has "Default role for new members".
- First-run setup creates the first instance admin.

## Sign-in methods

SMTP is **required** (setup wizard blocks until a test email succeeds).

1. **Magic link**: enter email → email with a single-use link and a 6-digit code (for when the link opens on another device). Token: 32 random bytes, stored hashed (SHA-256), 15 min TTL, single use, bound to the requesting browser via a short-lived cookie nonce (code entry works without it). Rate-limited per email and IP.
2. **Passkeys** (WebAuthn): users add passkeys after first sign-in from Profile. Discoverable credentials enable one-tap "Sign in with passkey". Multiple passkeys per user, named, with last-used. The relying party is `server.base_url` (RP ID = its host, the only allowed origin = its scheme and host), so changing the base URL's host invalidates passkeys. Resident keys are required, user verification preferred, a user's existing credentials are excluded when adding one, and a sign count going backwards (a cloned authenticator) refuses the sign-in and is audited. Challenges are single-use and expire after 5 minutes.
3. **Continue with GitHub / GitLab**: OAuth via the GitHub App's user OAuth and the GitLab OAuth application. Signs in only if the forge identity is already **linked** to a kmdn account, or if the forge's verified primary email matches an existing kmdn account (then it links automatically). Never creates accounts on its own, unless the auto-join domain rule matches.

No passwords.

## Sessions

- Server-side sessions in DB, cookie `kmdn_session` (HttpOnly, Secure, SameSite=Lax), 30-day rolling expiry, 12 h idle re-validation for admins.
- CSRF: SameSite=Lax + double-submit token header `X-Kmdn-CSRF` on unsafe methods.
- Users can list and revoke their sessions in Profile. Admins can revoke all sessions of a user.
- WebSocket upgrade authenticates via the same cookie and checks `Origin`.

## Linked accounts

- Linking GitHub/GitLab stores forge user id, login, avatar, verified emails, and the noreply address. Tokens from user OAuth are used only at link time (and, for GitLab, to create access tokens when requested) and are not stored long-term, except a GitLab token kept for 10 minutes during the token-creation flow.
- Linked identities power: sign-in, commit email (noreply), avatar, @mention hints.

## Roles (per repo)

| Capability | Viewer | Contributor | Maintainer | Admin |
|---|:-:|:-:|:-:|:-:|
| Read published docs, search, history, blame | ✓ | ✓ | ✓ | ✓ |
| Repo Q&A with assistant | ✓ | ✓ | ✓ | ✓ |
| Comment on published docs (discussions) | ✓ | ✓ | ✓ | ✓ |
| View revisions, comment in revisions | ✓ | ✓ | ✓ | ✓ |
| Create revisions, edit own/invited revisions, invite collaborators to own revisions | | ✓ | ✓ | ✓ |
| Prompt the assistant in revisions they can edit | | ✓ | ✓ | ✓ |
| Accept/reject suggestions in revisions they can edit | | ✓ | ✓ | ✓ |
| Edit any revision while Editing, be assigned as reviewer, publish Approved revisions, close any revision | | | ✓ | ✓ |
| Follow pages and folders | ✓ | ✓ | ✓ | ✓ |
| Run the consistency scan, ignore findings repo-wide | | | ✓ | ✓ |
| Repo settings, members, integrations, disconnect repo | | | | ✓ |

- **Instance admins** have Admin on every repo and access to the admin console.
- **Revision collaborators**: creator + invited users (must be Contributor+ on the repo; inviting a Viewer offers to upgrade them if the inviter is a repo Admin, otherwise sends a request to repo admins). "Request to edit" notifies the revision creator and maintainers.
- **Per-state rights** inside a revision (editors read-only while In review, only assigned reviewers edit, approve or request changes) are defined in [07 · Who can do what, by state](07-review.md#who-can-do-what-by-state). The role table above gives the ceiling; the revision state narrows it.
- Effective role = max(direct user role, roles from groups).

## Groups

- Instance-level groups managed by instance admins (e.g. "HR", "Engineering", "Docs team").
- A group can be granted a role on any repo. Repo admins can grant existing groups roles on their repo.
- No nesting in v1. No sync from IdPs in v1.

## Agent keys (MCP)

See [12 · MCP server](12-mcp.md). Keys are a separate principal type: not users, no sessions, read-only, published content only.

## Secrets

- Encrypted at rest with AES-256-GCM using a key-encryption key from `KMDN_SECRET_KEY` (32 bytes base64, required; `kmdn init` generates one). Envelope encryption: each secret row has its own data key encrypted by the KEK, enabling rotation via `kmdn admin rotate-secret-key`.
- Encrypted: GitHub App private key, webhook secrets, GitLab access tokens, OAuth client secrets, SMTP password, LLM API keys, outgoing webhook secrets.
- Agent keys and magic-link tokens are stored as hashes only.

## Security baseline

- Rate limits: sign-in endpoints, API per user (token bucket), assistant runs, MCP per key.
- Strict CSP for the SPA (`script-src 'self'`, no inline scripts; Mermaid/KaTeX bundled), `frame-ancestors 'none'`.
- Rendered markdown: raw HTML sanitized with DOMPurify client-side and an allowlist; `javascript:` URLs stripped. Server never renders HTML for other users.
- SVG uploads sanitized; served from `/assets/` with `Content-Disposition: inline`, `Content-Security-Policy: sandbox`.
- All authorization checks in the service layer (not just handlers), covered by a permission matrix test.
