# 16 · Organizations

An instance hosts one or more **organizations** ("orgs"). An org owns repositories, groups, invitations, agent keys, forge connections, settings and its audit log. People have one account per instance and join orgs as members. A self-hosted install runs with a single default org and looks the same as before orgs existed; an instance can also host many orgs, which is how a hosted service runs kmdn ([D61](decisions.md), [ADR 0004](../adr/0004-organizations.md)).

The core has no notion of plans, prices or billing. It exposes limits and policy hooks that an embedder fills ([Embedding](#embedding)).

## Modes

| `orgs.mode` | Behaviour |
|-------------|-----------|
| `single` (default) | Exactly one org, `org_default`, created by the migration or the setup wizard. Every active account is a member. Creating orgs is off; the org switcher is hidden; old URLs without an org redirect to it. |
| `multi` | Any number of orgs. Who may create one is decided by `orgs.allow_create` (`admins` by default, `anyone` for self-serve) or by the embedder. Accounts belong only to the orgs they joined. |

Switching `single` → `multi` keeps the default org as it is. The reverse is refused while more than one org exists.

## Data model

```
orgs         id (org_…), slug (unique), name, status (active|suspended|deleting),
             created_at, deleted_at?
org_members  org_id, user_id, role (owner|admin|member), status (active|deactivated),
             invited_by?, joined_at                          -- unique (org_id, user_id)
org_settings org_id, key, value (json), updated_at           -- pk (org_id, key)
org_domains  org_id, domain (unique), verified_at?           -- auto-join and SSO claims
```

**Root tables** get `org_id` (not null unless noted): `repos`, `forge_hosts` (null = shared by the instance, configured by the operator), `forge_installs`, `groups`, `invites`, `agent_keys`, `audit_log` (null = instance event), `secrets` (null = instance secret), `jobs` (null = instance job), `uploads`, `assistant_runs`, `assistant_threads`, `notifications`, `webhook_deliveries` (null until routed).

**Per-org uniqueness**: `groups (org_id, name)`, `repos (org_id, forge_host_id, owner, name)`, `uploads (org_id, sha256)`. Uploads are never deduplicated across orgs: a shared blob would tell one org that another has the same file.

**Global**: `users` (email unique), `sessions`, `magic_links`, `passkeys`, `linked_accounts`, `push_subscriptions`, `instance_settings`.

**Inherited**: everything keyed by `repo_id` or `revision_id` belongs to the repo's org. The service layer always reaches those rows through their repo or revision, so the org check happens once, where the parent is loaded. On Postgres, these tables also carry a denormalized `org_id` for row-level security ([Isolation](#isolation)).

The migration creates `org_default` (slug from the instance name, else `default`), backfills every root row, makes instance admins its owners and every other account a member.

## Roles

| Role | Scope | Can |
|------|-------|-----|
| **Instance admin** (`users.is_instance_admin`) | Instance | Instance settings (SMTP, AI provider, shared forges, system, doctor), list and suspend orgs. Reaching into an org's content is a break-glass action: allowed, and always written to that org's audit log |
| **Org owner** | Org | Everything an org admin can, plus managing owners, renaming or deleting the org |
| **Org admin** | Org | The org console: members, groups, invitations, repositories, org-owned forges and installations, AI budgets, agent keys, audit log, settings. Admin on every repo of the org |
| **Org member** | Org | Whatever their repo roles and groups grant ([09](09-auth-permissions.md#roles-per-repo)) |

Effective repo role = max(repo role, group roles, Admin if org admin or owner). A person has no role on a repo outside the orgs they're an active member of, whatever their grants say. Deactivating a member removes access to that org only and revokes their live sockets for it; instance-level deactivation (staff) signs the account out everywhere.

In `single` mode, instance admins are also owners of the default org, so self-hosted admins see no difference.

## URLs and the request context

**SPA**: `/{org}/{owner}/{repo}/…`. Top-level segments reserved for the app (`admin`, `settings`, `signin`, `signup`, `setup`, `invite`, `auth`, `api`, `ws`, `mcp`, `hooks`, `assets`, `healthz`, `readyz`, `metrics`, `version`, `orgs`, `new`) can't be org slugs. Slugs: 2–39 characters, lowercase letters, digits and single hyphens. In `single` mode, `/{owner}/{repo}` URLs redirect to `/{default}/{owner}/{repo}`.

**API**:

- Org collections live under `/api/v1/orgs/{org}/…` (`{org}` is the slug): repos list and connect, by-slug lookup, directory, groups, invitations, notifications, assistant status, and the org console under `/orgs/{org}/admin/…` (members, groups, audit, agent keys, forges, AI budgets and usage, settings).
- Resources addressed by ID (`/repos/{repo}`, `/revisions/{revision}`, `/threads/{thread}`, `/comments/{comment}`, `/assistant/threads/{thread}`, `/consistency/findings/{finding}`, `/invites/{token}`) keep their paths. The service loads the row, takes its org, and checks the caller's membership before anything else. A resource in an org the caller can't see answers **404, never 403**, so IDs can't be probed.
- Instance endpoints stay at `/api/v1/admin/…` (SMTP, system, jobs, AI provider, shared forges, `/admin/orgs`).
- `GET /orgs` lists my orgs; `POST /orgs` creates one when allowed; `GET|PATCH /orgs/{org}`; `GET /orgs/{org}/members`, `PATCH|DELETE /orgs/{org}/members/{user}`.

The resolved org travels in the request context (`orgs.From(ctx)`). Services never take an org ID from request input without resolving it through membership.

**Links**: emails, outgoing webhooks, push notifications, MCP results and OAuth state build URLs from `server.base_url` plus the org path, through one URL builder.

**WebSocket**: one connection per tab, as before. Subscribing to a channel (revision, room, inbox) checks the org of the resource and the caller's membership; deactivation closes those subscriptions.

## Sign-in and joining

- Accounts sign in to the instance (magic link, passkey, linked forge), then pick an org. The last org used is remembered per browser. The WebAuthn relying party stays the host of `server.base_url`, so a passkey works for every org.
- Invitations belong to an org (optionally with a repo and role). Accepting one adds the org membership.
- Auto-join by email domain is an org setting backed by `org_domains`; a domain belongs to at most one org.
- An account without an org sees "Ask an admin for an invitation", or the embedder's signup page in `multi` mode with self-serve.
- Sign-in methods, member provisioning and per-org sign-in policy are extension points ([Embedding](#embedding)).

## Settings

| Scope | Keys |
|-------|------|
| Instance (`instance_settings`) | setup state, instance name, SMTP, AI provider and keys, VAPID keys, shared forge hosts, OAuth and manifest states |
| Org (`org_settings`) | org display name, auto-join, default member role, assistant on/off, AI budgets, consistency scan schedule and caps, retention overrides |

Any setting can be **managed**: set by the config file, the environment or an embedder, then shown read-only in the console, as the AI provider already is when `assistant.provider` is in the config.

## Secrets

Org secrets (`secrets.org_id` set) include the org ID in the additional authenticated data, so a row can't be moved to another org. Their data keys are wrapped by an **org key**, which is wrapped by the instance key (`secret_key`). Deleting an org destroys its org key, which makes every forge token, webhook secret and hook URL of that org unreadable. `kmdn admin rotate-secret-key` re-wraps org keys; `kmdn admin rotate-org-key --org` re-wraps one org's data keys.

## Forges

- **Shared hosts** (`forge_hosts.org_id` null) are configured by the operator (config, env or embedder): typically one GitHub App on github.com for every org. Orgs can't edit them.
- **Installations**: "Connect GitHub" in an org opens the App's install page with a signed `state`; the App's setup callback claims the installation for that org. An installation belongs to one org; a second org gets "This GitHub account is already connected to another organization".
- **Webhooks** for shared hosts route by installation to its org, then to the repo. `installation` events create or remove claims.
- **Org-owned hosts**: GitHub Enterprise Server (manifest flow run by the org admin) and self-managed GitLab (OAuth app registered by the org admin). Plain git URLs are org-owned per repo and go through the URL policy ([Policy](#policy)).
- `linked_accounts` stay global: one GitHub identity links to one account, whatever the orgs.

## Uploads

Stored as `<org>/<sha[:2]>/<sha>` behind a blob store (`blobs.Store`: Put, Get, Delete, Stat) with a filesystem implementation (default) and an S3-compatible one. When `server.content_base_url` is set, raw files and uploads are served only there through short-lived signed URLs, and the app origin redirects to them; app cookies then use the `__Host-` prefix.

## Jobs and fairness

`jobs.org_id` scopes work. Claiming is fair across orgs (round-robin over orgs with ready jobs of a kind), with a per-org concurrency cap per kind (mirror fetch, embedding, consistency, assistant). The doc-engine pool has a per-org cap in front of it, so one org's large parse can't hold every runtime. Periodic work is enqueued per org with jitter.

## AI

`assistant_runs.org_id`; budgets are per user per day and **per org per month** (the former instance budget becomes the org budget; the instance keeps an optional global cap). An org setting (or the embedder) can turn every LLM feature off for an org. Provider and keys stay instance settings.

## Policy

A `Policy` decides what orgs may do. The default is permissive (self-hosted admins are trusted, [15](15-security.md) T10); a strict policy is for instances whose org admins aren't the operator:

- git URL schemes (strict: `https` and `ssh` only; `file://`, local paths and `ext::` refused; git runs with `protocol.allow=never` except for those);
- outbound addresses for git, forge APIs, AI and embeddings base URLs, OTLP and webhooks (strict: public addresses only, through one dialer);
- limits per org: members, repos, mirror size, upload size, AI on/off and budgets;
- which console sections org admins can edit.

A refused limit is an explicit state with its own message: an org over its member limit is **read-only** (read and comment; no new revisions, no publishing) until it's back under.

## Embedding

`pkg/kmdn` is the public surface for building kmdn into another Go program (the only way, since `internal/` can't be imported):

```go
app, err := kmdn.New(ctx, cfg,
    kmdn.WithPolicy(p),                 // limits, URL and egress rules, console sections
    kmdn.WithEvents(sink),              // member added/removed/role changed, usage, org lifecycle
    kmdn.WithRoutes(func(r chi.Router){…}), kmdn.WithAPIRoutes(…),
    kmdn.WithMigrations(fs, "schema"),  // extra migrations in their own schema
    kmdn.WithManagedSettings(m),
    kmdn.WithSignInProvider(sp),        // e.g. SAML; returns a verified identity
    kmdn.WithSignInPolicy(sip),         // per-org allowed sign-in methods
    kmdn.WithProvisioning(),            // create/deactivate memberships from an external source
    kmdn.WithBlobStore(bs), kmdn.WithMailer(m), kmdn.WithLogger(l), kmdn.WithMetrics(reg),
)
app.Orgs.Create(ctx, kmdn.NewOrg{Slug, Name, Owner})   // and Suspend, Resume, Export, Delete
http.ListenAndServe(addr, app.Handler())
```

An example embedder test in the core runs two orgs behind these options, so the surface can't break unnoticed.

## Isolation

1. **Service layer**: every org-scoped query filters by the org from the context; every ID-addressed resource is checked against the caller's memberships where it's loaded.
2. **Cross-org test matrix**: a fixture with two orgs calls every operation in `api/openapi.yaml` as members and admins of org A with org B's IDs and slugs and expects 404; it also covers WebSocket subscriptions, MCP keys, webhooks, search, the directory and mentions. Adding an operation without a cross-org case fails the test.
3. **Row-level security** on Postgres: `org_id` on every tenant table and a policy per table; requests set `app.org_id` with `SET LOCAL` per transaction; instance jobs use a role that bypasses it. SQLite relies on 1 and 2.

## Out of scope in the core

Plans, prices, payments, signup marketing pages, staff tooling and custom domains per org. They belong to whoever runs a multi-org instance, through the extension points above.
