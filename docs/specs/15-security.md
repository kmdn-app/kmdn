# 15 · Security review and threat model

A review of kmdn as built through M5 (issue #70). It lists what's worth protecting, who might attack it and where, what stands in the way, how that's tested, and what this review found and fixed. Revisit it when a trust boundary changes (a new integration, write access for agents, multi-node).

## Assets

| Asset | Where | Why it matters |
|-------|-------|----------------|
| Repository content (published and in revisions) | Git mirrors, `revision_files`, Yjs documents | Confidential documentation; tampering with it can mislead readers |
| Forge credentials (GitHub App keys, GitLab tokens), SMTP password, AI and embeddings keys, webhook secrets | `secrets` table, envelope-encrypted with `secret_key` | Access to the organization's repositories and services |
| Sessions, magic links, passkeys, invites | `sessions`, `magic_links`, `passkeys`, `invites` (hashes, except passkey public keys) | Account takeover |
| Agent keys | `agent_keys` (SHA-256 of the secret) | Read access to published docs from outside |
| Audit log | `audit_log` | Accountability; tampering hides abuse |
| The secret key | Config or env, never in the database or backups (unless `-include-secrets`) | Decrypts every stored credential |

## Actors

- **Anonymous internet user**: reaches sign-in, invites, webhooks, `/mcp`, `/healthz`, `/metrics` (if exposed).
- **Signed-in person** with no access, or a viewer, contributor, maintainer or admin on some repositories.
- **Instance admin**: trusted with configuration (forges, AI, SMTP, agent keys), but not with other people's credentials in clear.
- **External agent** holding an agent key.
- **Forge** (GitHub, GitLab): sends webhooks; trusted for content and identity it asserts in signed payloads.
- **Repository content itself**: written by anyone with push access to the repository, possibly outside kmdn. Treated as untrusted input when rendered and when read by the assistant.
- **AI provider**: receives prompts with repository content; its output is untrusted.

## Trust boundaries and entry points

1. Browser ↔ server: cookies (HttpOnly, SameSite=Lax, Secure on https), CSRF header on unsafe methods, WebSocket with an Origin check, CSP.
2. Server ↔ forges: outbound API calls with app or installation tokens; inbound webhooks with HMAC or token.
3. Server ↔ git: `git` subprocesses on mirrors; credentials are passed via `GIT_ASKPASS`, never on the command line or in URLs.
4. Server ↔ AI provider and embeddings endpoint: outbound HTTPS, admin-configured.
5. Server ↔ outgoing webhooks: admin- or maintainer-configured URLs.
6. Agents ↔ `/mcp`: bearer agent keys, read-only.
7. Operator ↔ CLI: backup, restore, doctor, key rotation (local access).

## Threats and mitigations

| # | Threat (STRIDE) | Mitigation | Verified by |
|---|-----------------|------------|-------------|
| T1 | A stranger or outsider calls an API route they shouldn't (E) | `auth.Require` on every non-public group; repo-scoped loads check `access.Effective`; admin groups use `RequireAdmin`; not found instead of forbidden for invisible repos | `TestAuthzMatrix` walks all 178 API routes as a stranger and as a signed-in outsider with real ids: only public or personal routes may succeed, nothing may 5xx. Plus per-feature permission tests |
| T2 | Cross-site request forgery (T) | CSRF token (cookie + `X-Kmdn-CSRF` header, constant-time) on unsafe methods; SameSite=Lax; WebSocket Origin must equal `base_url`; `/mcp` holds browsers to same-origin | Auth tests; `TestAuthzMatrix` (requests without the header fail) |
| T3 | Stored XSS through markdown, HTML blocks, math or diagrams (T, E) | Raw HTML through DOMPurify (no `style`, `form`, `input`, `style` attributes); KaTeX with `trust: false`; Mermaid `securityLevel: strict`; `javascript:` links dropped, other schemes open in a new tab with `noopener`; CSP `script-src 'self'`, `object-src 'none'`, `frame-ancestors 'none'` | Doc engine and web tests; CSP set on every response |
| T4 | XSS through files served from the repository or uploads (SVG, HTML) (T, E) | Raw endpoints serve an allowlist of types only, with `X-Content-Type-Options: nosniff` and `Content-Security-Policy: default-src 'none'; sandbox`; images render through `<img>`; with `server.content_base_url`, served only from a separate cookieless origin through signed URLs and app cookies use the `__Host-` prefix ([16](16-organizations.md#uploads)) | Code review; raw handlers |
| T5 | Account takeover through magic links or codes (S) | Single-use, short-lived, hashed tokens; rate limits per email and IP; the same answer whether or not the account exists; new session id on sign-in | `internal/auth` tests |
| T6 | Passkey phishing, cloned authenticators (S) | Relying party from `base_url` (origin checked); single-use challenges expiring after 5 minutes; a sign count going backwards refuses the sign-in and is audited | `TestPasskeys` (wrong origin, clone, unknown and removed keys) |
| T7 | Open redirect after sign-in (S) | Server-side redirects accept same-origin paths only (no `//`, no backslash); the sign-in page applies the same rule | Linking tests; this review fixed the web check (backslash) |
| T8 | Forged forge webhooks (S, T) | GitHub HMAC-SHA256 (`hmac.Equal`); GitLab and plain git tokens (constant time); bodies capped at 10 MiB; webhooks only trigger a fetch, whose truth comes from the forge | Forge tests |
| T9 | SSRF through outgoing webhooks (I) | A dialer that refuses private, loopback and link-local addresses (unless `hooks.allow_private`); only the host is stored in the audit log | Hooks tests |
| T10 | SSRF or exfiltration through admin-configured URLs (AI, embeddings, forge base URLs, OTLP) (I) | Admin-only; instance admins are trusted with configuration (org admins who aren't the operator: T20). Changes are audited (keys as "changed", never values) | Audit tests |
| T11 | Stored credentials read from the database or a backup (I) | AES-256-GCM envelope encryption with per-secret data keys under `secret_key`; the key isn't in the database; backups blank it unless asked; `kmdn doctor` detects secrets the key can't decrypt; key rotation re-wraps | Secrets and CLI tests |
| T12 | Credentials leaking into logs, errors or the audit log (I) | git passwords are redacted from errors; the audit log never stores secret values or webhook URL paths; internal errors return a generic 500 with a request id | Hooks and audit tests (no URL path in the audit log) |
| T13 | Prompt injection from repository content steering the assistant (T, E) | The assistant acts as the person asking, within their role; its edits are tracked suggestions people accept; file operations and new revisions need a person's confirmation; budgets cap cost; runs are audited | Assistant tests (viewer can't confirm; suggestions not applied) |
| T14 | Agent key leak (I) | Scoped per repo, expiring by default after 90 days, shown once, stored hashed, instantly revocable, rate limited (120/min, 10 concurrent), every call audited with IP and last use shown | `TestMCPServer` |
| T15 | Denial of service: huge bodies, huge documents, many editors (D) | JSON bodies capped at 1 MiB; Yjs updates at 1 MiB; 50 editors per file; doc engine calls under a time budget; job queue with backoff; magic link and MCP rate limits | Load test below; collab tests |
| T16 | Audit log tampering (R) | Append-only table written in the same transaction as the change it records (revision events); exports are themselves audited | Audit tests |
| T17 | Metrics disclosure (I) | `/metrics` can require a bearer token (`telemetry.metrics_token`); the example Caddyfile blocks it; it holds counts and ids, no content | `TestMetricsAndTraces` |
| T18 | Malicious git content attacking the server (T) | git runs with `GIT_CONFIG_NOSYSTEM`, a null global config, no prompts; mirrors are bare (no hooks run on fetch); paths with tabs or newlines are refused when committing; restores refuse `..` paths | gitmirror and backup tests |
| T19 | A member of one organization reads or changes another's data (I, T, E) | Org from the request context on every org-scoped query; ID-addressed resources checked against the caller's memberships where they're loaded, answering 404; per-org uniqueness (no cross-org upload dedupe); org keys for org secrets; Postgres row-level security as a second barrier ([16](16-organizations.md#isolation)) | Cross-org matrix over every API operation, WebSocket channel, MCP key and webhook |
| T20 | Org admins abusing admin-configured URLs or git URLs when they aren't the operator (I) | Strict policy: git over `https`/`ssh` only, no local paths, `protocol.allow=never` otherwise; one egress dialer refusing non-public addresses for forges, AI, embeddings and webhooks ([16](16-organizations.md#policy)) | Policy tests (`file:///`, local paths, `ext::`, link-local) |

## Findings of this review

| Finding | Severity | Status |
|---------|----------|--------|
| On private HTTPS forges, tree listings, indexing, publishing, history and blame ran git commands that lazily fetch blobs without credentials, so they failed (availability) | High (functional) | Fixed in #115: listings don't need blobs, `write-tree --missing-ok`, credentials for history and blame; covered by an HTTP partial-mirror test and the e2e suite |
| The sign-in page accepted `/\host` as a redirect (browsers treat it as another origin); not exploitable because `pushState` refuses cross-origin URLs | Low | Fixed: backslashes refused |
| `/metrics` had no authentication of its own | Low (counts and ids only) | Fixed: optional `telemetry.metrics_token`; the Caddyfile blocks it |
| Avatar and several theme colors were below WCAG contrast (not security) | n/a | Fixed in #116 |
| Tests only used `file://` remotes, hiding forge-only behavior | Process | The e2e suite runs against a fake GitLab over HTTP with a token |

No authorization gaps were found by the route matrix; the per-feature tests (revisions, reviews, assistant, consistency, MCP, admin) hold.

## Residual risks and operator guidance

- **Admins are trusted**: they can point AI, embeddings and forge URLs anywhere and read the audit log. Keep the admin group small.
- **`style-src 'unsafe-inline'`** remains for the UI's inline styles; scripts stay `'self'`.
- **Single node**: rate limits and WebAuthn challenges are in memory; restarting clears them (fine), multiple nodes would need shared state.
- **Repository content is untrusted**: the assistant may read instructions planted in docs; that's why it only suggests.
- **Put kmdn behind TLS** (`base_url` https): cookies get `Secure`, passkeys need it outside localhost, and `kmdn doctor` warns otherwise.
- **Protect the secret key** separately from backups; losing it means re-entering credentials, leaking it with a database dump exposes them.
- Open items that need real services: signed commits on GitHub and GHES (spike S5) and GitLab token rotation (S6).
