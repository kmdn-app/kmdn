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
- Sign-in methods, member provisioning and per-org sign-in policy are extension points ([Sign-in](#sign-in)).

## Sign-in

The core ships magic links, passkeys and linked forge accounts. An embedding program adds the rest (SAML, OIDC, directory sync) through three extension points, without patching `internal/auth`:

- **Providers** (`kmdn.WithSignInProvider`). A provider has an ID, a name and whether the sign-in page lists it. `Start` sends the browser to the identity provider with a state; `Callback` (GET or POST, for SAML) returns the `Identity` it vouches for (issuer, subject, email and whether it was verified, name, optionally an org) and the state. The core checks the state (a `__Host-` cookie bound to a single-use server record, ten minutes), then finds the account: a link in `identity_links` (issuer + subject), else an active account with that verified email (linked from then on), else, if the identity names an org and allows it, a new account that joins the org within its member limit. The session's method is `provider:<id>`. Unlisted providers are reached from an org's "sign in" link.
- **Sign-in policy** (`Policy.SignIn(ctx, orgID, method)`). It returns nil to accept a session in an org, or a `SignInRequired` with a message and a start URL. Every session records how it was signed in (`magic_link`, `magic_code`, `passkey`, `oauth_github`, `oauth_gitlab`, `invite`, `setup`, `provider:<id>`; empty for sessions from before). The policy applies wherever org access is decided (`orgs.Role`), so a refused session gets no role in that org: its routes answer 403 `sign_in_required` with the message and URL, ID-addressed resources 404, and `GET /orgs` marks the org with `sign_in` so the app shows the message and a button. A sign-in is refused outright when every org the person belongs to refuses its method. Org owners and instance admins are never refused (the break-glass path), and people in no org can always sign in.
- **Provisioning** (`app.Provisioner()`). `Upsert` adds or updates a member by email (creating the account; role; active or deactivated), `Remove` takes them out of the org and its groups, `EnsureGroup` and `SetGroupMembers` keep groups in step. No invitations are sent; member limits apply to people added, the last active owner can't be demoted or removed, and every change is audited with its source (`"via": "scim"`) and reported as an event.

Agent keys and MCP tokens aren't sessions and aren't subject to the sign-in policy; an org that wants SSO only should limit who can create them.

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

Stored as `<org>/<sha[:2]>/<sha>` behind a blob store (`blobs.Store`: Put, Get, Delete, Stat) with a filesystem implementation (default) and an S3-compatible one. When `server.content_base_url` is set, raw files and uploads are served only there through short-lived signed URLs, and the app origin checks access and redirects to them. A signed URL names fixed bytes (a commit sha or an upload hash), is stable for one five-minute window and valid for at most two, and the content origin reads and sets no cookies. Over https, app cookies use the `__Host-` prefix so no other host on the site can set or overwrite them; cookies set before the prefix are moved to it, except next to a content origin, where only prefixed cookies count. Strict mode requires a content origin.

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

A limit reached is an explicit answer (409 `limit_reached`, with what and how many): members (pending invitations count, and auto-join stops at the limit), repositories, upload size. A **suspended** org (status and a reason, set by an instance admin or the embedder) is read-only: everyone's repo role is at most Viewer (read and comment; no new revisions, no publishing), org admins keep the console, and members see the reason in a banner. Config: `policy.strict` and `policy.no_org_forges`; limits come from the embedder.

## Embedding

`pkg/kmdn` is the public surface for building kmdn into another Go program (the only way, since `internal/` can't be imported):

```go
cfg, _ := kmdn.LoadConfig("")               // or kmdn.DefaultConfig()
app, err := kmdn.New(ctx, cfg, logger,
    kmdn.WithPolicy(&kmdn.Policy{Strict: true, Limits: planLimits}), // URL/egress rules, members, repos, upload size
    kmdn.WithManagedSettings(planSettings),    // e.g. {"assistant": false, "monthly_tokens": 100000}, read-only in the console
    kmdn.WithEvents(onEvent),                  // org created/status, member added/changed/removed, repo connected/removed, AI usage
    kmdn.WithRoutes(func(r chi.Router) {…}),   // root: sign-up pages, billing webhooks
    kmdn.WithAPIRoutes(func(r chi.Router) {…}),// /api/v1, kmdn.CurrentUser(r)
    kmdn.WithOrgRoutes(func(r chi.Router) {…}),// /api/v1/orgs/{org}, kmdn.CurrentOrg(r); outsiders get 404
    kmdn.WithMigrations(migrationsFS, "saas_migrations"),
    kmdn.WithSignInProvider(samlForAcme),      // Info, Start, Callback → kmdn.Identity
)
org, _ := app.CreateOrg(ctx, kmdn.NewOrg{Name: "Acme", OwnerEmail: "alice@acme.dev"})
app.SuspendOrg(ctx, org.ID, "Payment failed.")   // read-only, reason shown; ResumeOrg lifts it
app.SignIn(w, r, "alice@acme.dev")               // after the program verified the address itself
app.Seats(ctx, org.ID); app.OrgSettings(ctx, org.ID); app.DB()
http.ListenAndServe(addr, app.Handler())          // or app.Run(ctx)
```

`pkg/kmdn/kmdn_test.go` is an embedding program that uses only this package (two orgs, its own routes, migrations, settings, limits, events, a sign-in provider and provisioning), so the surface can't break unnoticed. Sign-in providers, the sign-in policy and the provisioner are described in [Sign-in](#sign-in).

## Isolation

1. **Service layer**: every org-scoped query filters by the org from the context; every ID-addressed resource is checked against the caller's memberships where it's loaded.
2. **Cross-org test matrix**: a fixture with two orgs calls every operation in `api/openapi.yaml` as members and admins of org A with org B's IDs and slugs and expects 404; it also covers WebSocket subscriptions, MCP keys, webhooks, search, the directory and mentions. Adding an operation without a cross-org case fails the test.
3. **Row-level security** on Postgres: `org_id` on every tenant table and a policy per table; requests set `app.org_id` with `SET LOCAL` per transaction; instance jobs use a role that bypasses it. SQLite relies on 1 and 2.

## Out of scope in the core

Plans, prices, payments, signup marketing pages, staff tooling and custom domains per org. They belong to whoever runs a multi-org instance, through the extension points above.
