# 13 · Operations

## Distribution

- Single static binary `kmdn` (linux/darwin × amd64/arm64), `CGO_ENABLED=0`. Requires `git` ≥ 2.40 on PATH.
- Docker image `ghcr.io/<org>/kmdn:<version>` (multi-arch), non-root user, `/data` volume, includes git.
- Example `deploy/docker-compose.yml` (kmdn + optional Postgres + optional Caddy for TLS, as compose profiles) with `deploy/kmdn.env.example`.
- Releases: GoReleaser on `v*` tags (`.goreleaser.yaml`, `.github/workflows/release.yml`) builds the binaries and a linux/amd64+arm64 image (`Dockerfile.release`, Alpine + git, uid 10001, healthcheck on `/healthz`). `Dockerfile` builds the same image from source; CI validates the release config and builds and smoke-tests that image on every PR.
- Behind a reverse proxy by default; can terminate TLS itself with ACME (`server.tls.acme`) for simple installs.

## CLI

```
kmdn serve                         run the server
kmdn init                          write kmdn.yaml skeleton + generate secret key
kmdn migrate [up|status]           DB migrations (also run automatically on serve)
kmdn doctor                        check git version, data dir perms, DB, SMTP, forge reachability, LLM
kmdn admin create-invite --email   bootstrap/recovery when locked out
kmdn admin rotate-secret-key
kmdn admin rotate-org-key -org SLUG
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
  url: sqlite:///data/kmdn.db        # or postgres://… as an ordinary role (not a superuser, which skips row-level security)
smtp:
  host: smtp.postmarkapp.com
  port: 587
  username: …
  password: ${KMDN_SMTP_PASSWORD}
  from: "kmdn <kmdn@northwind.dev>"
auth:
  auto_join_domains: []             # single mode: the default org's auto-join
  session_ttl: 720h
orgs:
  mode: single                       # or multi (see 16-organizations.md)
  allow_create: admins               # multi mode: admins | anyone
  signup_url: ""                     # an embedder's signup page, linked from sign-in and the no-org page
policy:                              # 16-organizations.md#policy
  strict: false                      # org admins aren't the operator: git over https/ssh to public hosts, org forges and webhooks to public addresses
  no_org_forges: false               # orgs only use the instance's shared forges
assistant:
  enabled: true
  # Optional: fix the provider here instead of the admin console (then shown locked).
  provider: anthropic                # or openai; empty: set in the admin console
  api_key: ${KMDN_ASSISTANT_API_KEY}
  base_url: ""                       # empty: the provider's API (openai: https://api.openai.com/v1)
  model: ""                          # assistant; empty: the provider's default
  review_model: ""
  short_model: ""
  embeddings:                        # consistency checks; set model to fix them here
    base_url: ""                     # empty: https://api.openai.com/v1
    api_key: ""
    model: ""                        # e.g. text-embedding-3-small
limits:
  upload_max_mb: 10
  wysiwyg_max_file_mb: 1
telemetry:
  metrics: true
  metrics_token: ""               # optional: require Authorization: Bearer <token> on /metrics
  otlp_endpoint: ""
  log_format: json
```

Forge connections, LLM providers and SMTP can also be managed in the admin console; values from config/env take precedence and are shown as locked in the UI. With `assistant.provider` set, the provider, key, base URL and models come from the config (budgets and scan settings stay editable), and kmdn runs the capability check at startup; a key the provider refuses turns the assistant off until the next restart or "Save and check".

On Upsun, `deploy/upsun/kmdn` derives the base URL, database, secret key and mail relay from the platform; see [deploy/upsun/README.md](../../deploy/upsun/README.md).

## First-run setup wizard

Served when no instance admin exists (protected by a one-time setup token printed in the server log to prevent hijacking a fresh instance):

1. **Admin account**: name + email, and the organization's name (the default org in `single` mode).
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

- `kmdn backup`: a consistent online copy of the SQLite database (`VACUUM INTO`, safe while the server runs; for Postgres, back up with `pg_dump` and `kmdn backup -skip-db` bundles the rest), uploads and the config file with `secret_key` and passwords blanked (unless `-include-secrets`). Collaborative documents (Yjs updates and snapshots) live in the database. Mirrors are excluded. The archive is `.tar.zst` (or `.tar.gz` by extension) with a `manifest.json` first (format, kmdn version, schema version, counts).
- `kmdn restore -in FILE [-force]`: refuses while a server answers on `server.listen`, refuses to replace an existing database without `-force`, restores the database and uploads, writes the backed-up config to `<data_dir>/kmdn.yaml.restored` (never over the live one), deletes mirrors and queues a sync of every repo so they're cloned again on the next start.
- Restore stages and validates the complete archive, required entries, counts, compression checksum and SQLite integrity before replacing data. Invalid backups leave the existing database, WAL, uploads and mirrors intact. Staging requires temporary disk space for the expanded backup. Files are then installed individually; an I/O failure during installation can still require retrying the restore.
- `kmdn doctor [-offline]` (and Admin → System → Doctor): git ≥ 2.40, data dir writable and free space, `base_url` (https unless local), database and pending migrations, the secret key (decrypts every stored secret, naming the kinds that fail), failed jobs, and, online, SMTP reachability, each forge's API, and the AI provider as `serve` resolves it, the config's over the console's (reachable, last capability check). Exits non-zero when a check fails.
- Documented recovery: the secret key is required to decrypt stored credentials; losing it means re-entering forge/SMTP/LLM secrets.

## Upgrades

- Semver. DB migrations forward-only, run on start with a lock; `kmdn migrate status` before upgrading.
- Doc engine version recorded on Y.Docs; engine upgrades that change schema ship with migrations applied lazily when a room loads.

## License and project

- AGPL-3.0 for the whole repo, copyright nlsio LLC. [`CONTRIBUTING.md`](../../CONTRIBUTING.md) and a [Contributor License Agreement](../../CLA.md) accepted once per contributor through a CLA bot (`.github/workflows/cla.yml`, signatures on the `cla-signatures` branch). The CLA replaces the DCO sign-off planned earlier: it lets nlsio LLC also distribute contributions under other terms, while kmdn stays AGPL. Security policy with private disclosure.
- CI (GitHub Actions): Go lint/test (race), TS typecheck/lint/test, fidelity corpus in both hosts, Playwright E2E against the built binary with a fake forge server, GoReleaser on tags.

## Capacity

`pnpm --filter @kmdn/e2e load` (`e2e/load/load.ts`) starts the same stack as the e2e suite (the built binary and the fake forge, SQLite) and drives real WebSocket editors with Yjs, then open revisions. Measured on a laptop (Apple Silicon, one process):

| Scenario | Result |
|----------|--------|
| 50 editors on one page, a burst each every ~1 s for 20 s (864 edits) | Edit → peer p50 3 ms, p95 8 ms, p99 10 ms; all copies converged; every edit materialized to markdown; 0 errors; ~0.1 CPU core |
| 50 editors, a burst each every ~150 ms (4,111 edits, ~200/s) | p50 2 ms, p99 5 ms; converged; ~0.3 CPU core; 31 MB Go heap |
| 500 open revisions (created at ~126/s through the API) | Revisions list and picker p50 11 ms; a revision 2 ms; repo tree 7 ms; inbox 2 ms; search 2 ms |

The limits that bound a file (50 concurrent editors, 1 MiB updates) are enforced by the room; beyond one node, see [03](03-architecture.md).
