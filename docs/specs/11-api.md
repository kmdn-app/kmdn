# 11 · API

## Principles

- `api/openapi.yaml` (OpenAPI 3.1) is the source of truth. Go handlers are hand-written; `internal/app` has a test that fails when registered routes and documented operations differ. TypeScript types are generated with `openapi-typescript` into `packages/api-client` and used through `openapi-fetch`; CI checks the generated file is current.
- Base path `/api/v1`. JSON. Cookie session auth for the SPA. (Personal API tokens for scripts are post-v1; external agents use [MCP](12-mcp.md).)
- Errors: RFC 9457 `application/problem+json` with a stable `code` (e.g. `revision_not_approved`, `conflicts_pending`) and i18n-friendly `params`.
- Pagination: cursor-based (`?cursor=…&limit=…`, response `{items, next_cursor}`).
- Optimistic concurrency on mutable resources via `ETag` / `If-Match` (revision metadata, settings).
- Long-running actions (publish, connect repo, restore) return `202` with a job id; progress arrives on the event channel and via `GET /jobs/{id}`.

## Resources (v1)

With organizations ([16](16-organizations.md#urls-and-the-request-context)), collections move under `/orgs/{org}/…` (for example `GET /orgs/{org}/repos`, `/orgs/{org}/admin/audit`) and resources addressed by ID keep the paths below. `GET /orgs` lists the caller's orgs.

```
Auth
  POST   /auth/magic-link                     {email}
  POST   /auth/magic-link/verify              {token | email+code}
  POST   /auth/passkey/options  · /auth/passkey/verify
  GET    /auth/oauth/{forge}/start · /auth/oauth/{forge}/callback
  POST   /auth/logout
  GET    /me   PATCH /me
  GET    /me/sessions   DELETE /me/sessions/{id}
  GET    /me/passkeys   POST /me/passkeys/register/options|verify   DELETE /me/passkeys/{id}
  GET    /me/linked-accounts   DELETE /me/linked-accounts/{id}
  GET    /invites/{token}   POST /invites/{token}/accept

Repos
  GET    /repos                               repos visible to me
  POST   /repos                               connect (admin) → 202 job
  GET    /repos/{repo}   PATCH /repos/{repo}  DELETE /repos/{repo}
  GET    /repos/{repo}/health   POST /repos/{repo}/refresh
  GET    /repos/{repo}/members  PUT /repos/{repo}/members/{principal}  DELETE …
  GET    /repos/{repo}/tree?path=&revision=
  GET    /repos/{repo}/files/{path}?revision=&sha=          markdown + metadata
  GET    /repos/{repo}/files/{path}/history
  GET    /repos/{repo}/files/{path}/blame
  GET    /repos/{repo}/compare?path=&from=&to=
  GET    /repos/{repo}/search?q=&revision=
  GET    /repos/{repo}/templates
  GET    /repos/{repo}/discussions?path=&state=
  POST   /repos/{repo}/discussions                       {path, anchor, body}

Revisions
  GET    /repos/{repo}/revisions?state=&mine=&review_requested=
  POST   /repos/{repo}/revisions                            {title, description?, from_template?}
  GET    /revisions/{revision}   PATCH /revisions/{revision}        title, description
  POST   /revisions/{revision}/submit {reviewers[]} | /withdraw | /approve | /request-changes {note} | /publish | /close | /reopen
  GET    /revisions/{revision}/reviewers   PUT|DELETE /revisions/{revision}/reviewers/{user}   POST /revisions/{revision}/ask-to-review
  GET    /revisions/{revision}/reviewer-suggestions
  GET    /revisions/{revision}/updates                         pending update from Published: pages, incoming hunks, conflicts
  POST   /revisions/{revision}/updates/{id}/apply              → 202 job
  GET    /revisions/{revision}/files
  POST   /revisions/{revision}/files                           {op: add|rename|delete|modify, path, from_path?, template?}
  POST   /revisions/{revision}/files/rename-preview            {from, to} → link rewrites
  GET    /revisions/{revision}/diff?path=&mode=rendered|source
  GET    /revisions/{revision}/checks                          broken links, pending suggestions, conflicts, pending updates, consistency findings
  GET    /revisions/{revision}/commit-preview                  message + trailers
  POST   /revisions/{revision}/assets                          multipart upload → {path, markdown}
  GET    /revisions/{revision}/members  POST …  DELETE …  POST /revisions/{revision}/request-edit
  GET    /revisions/{revision}/checkpoints  POST /revisions/{revision}/checkpoints  POST /revisions/{revision}/checkpoints/{c}/restore
  GET    /revisions/{revision}/events
  GET    /revisions/{revision}/threads   POST /revisions/{revision}/threads
  POST   /revisions/{revision}/suggestions/{id}/accept | /reject   POST /revisions/{revision}/suggestions/bulk

Threads
  GET    /threads/{thread}   PATCH (resolve/reopen)
  POST   /threads/{thread}/comments   PATCH|DELETE /comments/{id}   PUT|DELETE /comments/{id}/reactions/{kind}
  POST   /threads/{thread}/fix-this                      → creates revision (discussions only)

Assistant
  GET    /repos/{repo}/assistant/threads                 my private Q&A threads
  POST   /repos/{repo}/assistant/threads
  GET    /revisions/{revision}/assistant                       the shared thread
  GET    /assistant/threads/{id}/messages
  POST   /assistant/threads/{id}/messages                {content, context:{path?, selection?}} → run id
  POST   /assistant/runs/{id}/cancel
  POST   /assistant/threads/{id}/start-revision             {proposal_id} → revision
  POST   /revisions/{revision}/review-summary                  → run id

Notifications
  GET    /notifications?unread=   POST /notifications/read {ids|all}
  GET    /me/notification-prefs   PUT …
  POST   /me/push-subscriptions   DELETE /me/push-subscriptions/{id}      Web Push (VAPID public key at GET /push/vapid-key)
  GET    /me/follows   PUT|DELETE /repos/{repo}/follows?path=&folder=
  POST   /repos/{repo}/reads {path, sha}                       records a page read (debounced client-side)
  GET    /repos/{repo}/files/{path}/since-last-visit           summary + changed ranges, if updated

Knowledge
  GET    /repos/{repo}/graph?scope=published|revision:<id>&folder=&duplicates=
  GET    /repos/{repo}/files/{path}/links                      backlinks, outgoing, local graph (2 hops)
  GET    /repos/{repo}/consistency?kind=&status=   PATCH /consistency/{finding} (ignore/reopen)
  POST   /repos/{repo}/consistency/scan                        → 202 job
  POST   /consistency/{finding}/fix                            → creates a revision, briefs the assistant

Admin (instance admins)
  GET/POST/PATCH /admin/users · POST /admin/users/{id}/deactivate|reactivate|revoke-sessions
  GET/POST/PATCH/DELETE /admin/groups · PUT/DELETE /admin/groups/{id}/members/{user}
  GET/POST/PATCH /admin/forges   POST /admin/forges/github/manifest
  GET/PUT /admin/settings        instance settings (auto-join domains, limits, retention)
  GET/PUT /admin/smtp            POST /admin/smtp/test
  GET/PUT /admin/llm             POST /admin/llm/test   GET /admin/llm/usage
  GET/POST /admin/agent-keys     DELETE /admin/agent-keys/{id}   GET /admin/agent-keys/{id}/usage
  GET /admin/audit?actor=&action=&repo=&from=&to=   GET /admin/audit/export (NDJSON/CSV)
  GET/POST/PATCH/DELETE /repos/{repo}/hooks  POST /repos/{repo}/hooks/{id}/test

Setup (only before first admin exists)
  GET /setup/status   POST /setup/admin   …

Misc
  GET /jobs/{id}   GET /healthz   GET /readyz   GET /metrics   GET /version
  GET /assets/{repo}/{revision?}/{path}                     serves images (revision uploads or mirror blobs)
```

## WebSocket `/ws`

See [05 · WebSocket protocol](05-collaboration.md#websocket-protocol). Event payloads (`kind 3`) are typed in OpenAPI under `components.schemas.Event*` (discriminated by `type`) so the TS client gets them generated too:

- `revision.updated` (state, flags, title, members, reviewers, approvals), `revision.files_changed`, `revision.update_available`, `revision.update_applied`, `revision.conflicts_changed`, `revision.published`, `revision.mode_changed`
- `thread.created|updated`, `comment.created|updated|deleted`, `suggestion.created|decided`
- `notification.created`
- `repo.health_changed`, `repo.head_changed`
- `job.progress|done|failed`

Agent stream (`kind 4`): `run.started`, `text.delta`, `tool.started {name, summary}`, `tool.finished`, `suggestions.created {count, files}`, `run.finished {usage}`, `run.failed`.

## Outgoing webhooks

Per repo, configured by repo admins. Events: `revision.submitted`, `revision.approved` (all reviewers), `revision.update_available`, `consistency.finding`, `revision.changes_requested`, `revision.published`, `revision.closed`, `discussion.created`.

- **Generic**: `POST` JSON `{id, type, created_at, repo, revision, actor, data}`, header `X-Kmdn-Signature: sha256=<hmac>`, `X-Kmdn-Event`, `X-Kmdn-Delivery`. Retries with exponential backoff for 24 h; delivery log in the UI with redeliver.
- **Slack**: Incoming Webhook URL, messages formatted with Block Kit (title, state, actor, link, files count).
