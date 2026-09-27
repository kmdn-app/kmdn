# 13 · Operations

## Distribution

- Single static binary `kmdn` (linux/darwin × amd64/arm64), `CGO_ENABLED=0`. Requires `git` ≥ 2.40 on PATH.
- Docker image `ghcr.io/<org>/kmdn:<version>` (multi-arch), non-root user, `/data` volume, includes git.
- Example `docker-compose.yml` (kmdn + optional Postgres + optional Caddy for TLS).
- Behind a reverse proxy by default; can terminate TLS itself with ACME (`server.tls.acme`) for simple installs.

## CLI

```
kmdn serve                         run the server
kmdn init                          write kmdn.yaml skeleton + generate secret key
kmdn migrate [up|status]           DB migrations (also run automatically on serve)
kmdn doctor                        check git version, data dir perms, DB, SMTP, forge reachability, LLM
kmdn admin create-invite --email   bootstrap/recovery when locked out
kmdn admin rotate-secret-key
kmdn backup  --out file.tar.zst    consistent backup (DB online backup + uploads + ydocs)
kmdn restore --in file.tar.zst
kmdn version
```

## Configuration

`kmdn.yaml`, every key overridable by env (`KMDN_SERVER_BASE_URL`, `KMDN_DB_URL`, …):

```yaml
server:
  base_url: https://kmdn.northwind.dev
  listen: :8080
  trusted_proxies: [10.0.0.0/8]
  tls: { acme: false }
data_dir: /data
secret_key: ${KMDN_SECRET_KEY}
db:
  url: sqlite:///data/kmdn.db        # or postgres://…
smtp:
  host: smtp.postmarkapp.com
  port: 587
  username: …
  password: ${KMDN_SMTP_PASSWORD}
  from: "kmdn <kmdn@northwind.dev>"
auth:
  auto_join_domains: []
  session_ttl: 720h
assistant:
  enabled: true                      # providers configured in admin console
limits:
  upload_max_mb: 10
  wysiwyg_max_file_mb: 1
telemetry:
  metrics: true
  otlp_endpoint: ""
  log_format: json
```

Forge connections, LLM providers and SMTP can also be managed in the admin console; values from config/env take precedence and are shown as locked in the UI.

## First-run setup wizard

Served when no instance admin exists (protected by a one-time setup token printed in the server log to prevent hijacking a fresh instance):

1. **Admin account**: name + email.
2. **Email (SMTP)**: settings + "Send test email" (must succeed; the admin's magic link is sent through it).
3. **Forge**: "Create GitHub App" (manifest flow, github.com or GHES URL) and/or "Add GitLab" (host URL, OAuth app ID/secret). Skippable.
4. **Assistant**: provider, key, model, "Test". Skippable.
5. **Done** → connect first repo.

## Admin console

Sections: **Users** (invite, deactivate, admin toggle, sessions, linked accounts, last active), **Groups**, **Repositories** (all repos, health, mirror size, last fetch, webhook status, token expiry, open revisions), **Forges** (GitHub App / GitLab hosts, credentials status), **Assistant** (providers, models per task, embeddings model, budgets, usage by user/repo over time, consistency scan schedule and caps), **Agent keys** ([MCP](12-mcp.md)), **Email** (SMTP settings + test), **Settings** (auto-join, retention, limits), **Audit log**, **System** (version, DB, disk usage, job queue depth and failures with retry, doctor results).

## Notifications

Two audiences, no notification emails. Email stays for sign-in and invites only.

### Participants, in real time

People involved in a revision (editors, assigned reviewers, commenters, @mentioned people) get **inbox** items delivered over the WebSocket, plus optional **browser push** (Web Push with VAPID keys generated at `kmdn init`; users opt in per browser from Profile; the PWA manifest makes it installable).

| Event | Who |
|---|---|
| Mentioned | the mentioned person |
| Review requested / re-requested | assigned reviewers |
| Reviewer edited during review | revision editors, other reviewers (grouped per reviewer per 10 min) |
| Approved (n of m) / Changes requested | editors, other reviewers |
| Updates from Published available | editors (Editing) or assigned reviewers (In review / Approved) |
| Back to editing with conflicts | editors, reviewers |
| Published / Publishing (PR opened) / PR closed | editors, reviewers |
| Edit access requested / Invited to a revision | revision creator + maintainers / the invitee |
| Reply in a thread you're in | thread participants |
| Consistency finding on your revision | editors |
| Repo disconnected, token expiring, scan failed | repo and instance admins |

- **Grouping**: per revision, collapsed ("3 updates in Update onboarding for 2026"). Push notifications are coalesced: at most one push per revision per 5 minutes, and none for events the person caused.
- **Quiet**: no push while the person has the revision open (they see it live; v1 uses presence in the revision's rooms rather than tab focus).
- Preferences per event kind: inbox on/off, push on/off.

### Readers of published pages

- **Updated since your last visit**: kmdn records when each user last read each page (`page_reads`, debounced). Opening a page whose published version changed since then shows a banner: "Updated Sep 26 by Tom Okafor with Priya · <1–2 sentence summary> · Show what changed". The summary is written by the assistant at publish time (stored per commit and file; falls back to the commit titles when no AI provider, which is all v1 does until M4). "Show what changed" highlights the changed blocks in place.
- **Follow** pages or folders (bell in the top bar, or on a folder in the tree). Followers get an inbox item (and push if enabled) when a revision touching them is published, with the same summary. Editors of a page are auto-followers for 30 days after it publishes; everyone can unfollow.
- Home shows "Updated since your last visit" for followed pages.

### Outgoing

Per-repo webhooks and Slack messages (see [11](11-api.md#outgoing-webhooks)) for teams that want events in their own tools.

## Audit log

Append-only table ([10](10-data-model.md)). Recorded actions include: sign-in (method, success/failure), session revoked, user invited/deactivated/role changed, group changes, repo connected/settings changed/disconnected, revision submitted/approved/changes requested/published/closed, suggestion accepted/rejected (count only per batch), assistant run (tools called, tokens), agent key created/revoked/used (per tool call), secrets changed (never values), setup completed. Filter by actor, action, repo, date; export NDJSON/CSV.

## Observability

- `/metrics` (Prometheus): HTTP latency/status, WS connections, active rooms, Yjs update rate, JS runtime pool usage and call latency, job queue depth/latency/failures, mirror fetch duration, publish outcomes, assistant tokens and latency by provider/model, MCP calls by tool and key.
- Structured `slog` JSON logs with request id, user id, repo id, revision id.
- OpenTelemetry traces (OTLP/HTTP) optional: HTTP, jobs, git commands, forge API calls, LLM calls. `telemetry.otlp_endpoint` is a collector's base URL (`http://otel-collector:4318`, `/v1/traces` is added) or a full traces URL; incoming `traceparent` headers are honored, and trace context is never sent to forges.
- Metric names: `kmdn_http_requests_total` / `kmdn_http_request_duration_seconds` (by route pattern), `kmdn_websocket_connections`, `kmdn_active_rooms`, `kmdn_yjs_updates_total`, `kmdn_engine_runtimes{,_in_use}` / `kmdn_engine_call_duration_seconds`, `kmdn_jobs_queued{kind}`, `kmdn_jobs_failed{kind}`, `kmdn_job_runs_total{kind,outcome}`, `kmdn_job_duration_seconds`, `kmdn_job_delay_seconds`, `kmdn_git_command_duration_seconds{command}` (fetch is the mirror fetch), `kmdn_forge_request_duration_seconds{host,code}`, `kmdn_publish_total{outcome}`, `kmdn_llm_tokens_total{provider,model,type}`, `kmdn_llm_call_duration_seconds`, `kmdn_mcp_calls_total{tool,key}`. `/metrics` is unauthenticated when `telemetry.metrics` is on: keep it off the public internet (reverse proxy rule or firewall).
- `/healthz` (process up), `/readyz` (DB ok, data dir writable, engine pool warmed).

## Backup and restore

- `kmdn backup`: SQLite online backup API (or `pg_dump` invocation guidance for Postgres, where `kmdn backup --skip-db` bundles the rest), uploads, ydocs dir, config (without secrets unless `--include-secrets`). Mirrors are excluded (re-cloned on restore).
- `kmdn restore`: stops if the server is running, restores, marks mirrors for re-clone.
- Documented recovery: the secret key is required to decrypt stored credentials; losing it means re-entering forge/SMTP/LLM secrets.

## Upgrades

- Semver. DB migrations forward-only, run on start with a lock; `kmdn migrate status` before upgrading.
- Doc engine version recorded on Y.Docs; engine upgrades that change schema ship with migrations applied lazily when a room loads.

## License and project

- AGPL-3.0 for the whole repo. `CONTRIBUTING.md` with DCO sign-off. Security policy with private disclosure.
- CI (GitHub Actions): Go lint/test (race), TS typecheck/lint/test, fidelity corpus in both hosts, Playwright E2E against the built binary with a fake forge server, GoReleaser on tags.
