# kmdn on Upsun

`.upsun/config.yaml` runs kmdn as one Go app with PostgreSQL 17 and a storage mount (`data/`) for git mirrors and uploads. The build installs the web dependencies, builds the SPA, embeds it and compiles `bin/kmdn`. At start, `deploy/upsun/kmdn` derives kmdn's settings from the platform and runs `bin/kmdn serve`.

## Derived settings

Any of these can be replaced by setting the variable yourself (`env:KMDN_…`).

| kmdn setting | Derived from |
|---|---|
| `KMDN_SERVER_BASE_URL` | the environment's primary route (`PLATFORM_ROUTES`) |
| `KMDN_SERVER_LISTEN` | `:$PORT` |
| `KMDN_DB_URL` | the `database` relationship (`DATABASE_*`) |
| `KMDN_DATA_DIR` | `$PLATFORM_APP_DIR/data` (the mount) |
| `KMDN_SECRET_KEY` | a SHA-256 of `PLATFORM_PROJECT_ENTROPY`, stable for the project's life |
| `KMDN_SMTP_HOST`, `_PORT`, `_SECURITY`, `_FROM` | Upsun's mail relay (`PLATFORM_SMTP_HOST`, port 25), from `kmdn@<route host>`; only on environments with outgoing mail (production by default) |
| `KMDN_SERVER_TRUSTED_PROXIES` | everything (only the Upsun router reaches the app) |
| `KMDN_TELEMETRY_METRICS` | `false` (set `true` plus `KMDN_TELEMETRY_METRICS_TOKEN` to expose `/metrics`) |

## Variables to set

The assistant uses Anthropic or OpenAI, set here rather than in the admin console:

```bash
upsun variable:create --level project --name env:KMDN_ASSISTANT_PROVIDER --value anthropic
upsun variable:create --level project --name env:KMDN_ASSISTANT_API_KEY --sensitive true --value 'sk-ant-…'
```

Optional:

| Variable | Default |
|---|---|
| `KMDN_ASSISTANT_MODEL` | the provider's (Anthropic: `claude-sonnet-5`) |
| `KMDN_ASSISTANT_REVIEW_MODEL`, `KMDN_ASSISTANT_SHORT_MODEL` | the provider's |
| `KMDN_ASSISTANT_BASE_URL` | the provider's API; for OpenAI-compatible servers |
| `KMDN_ASSISTANT_EMBEDDINGS_MODEL`, `_API_KEY`, `_BASE_URL` | none (consistency checks off); Anthropic has no embeddings, so use an OpenAI key and e.g. `text-embedding-3-small` |

Your own SMTP server instead of the relay:

```bash
upsun variable:create --level project --name env:KMDN_SMTP_HOST --value smtp.example.com
upsun variable:create --level project --name env:KMDN_SMTP_PORT --value 587
upsun variable:create --level project --name env:KMDN_SMTP_USERNAME --value kmdn
upsun variable:create --level project --name env:KMDN_SMTP_PASSWORD --sensitive true --value '…'
upsun variable:create --level project --name env:KMDN_SMTP_FROM --value 'kmdn <kmdn@example.com>'
```

`KMDN_SMTP_SECURITY` is `starttls` by default (`tls` for port 465). SMTP and AI settings from variables show as locked in the admin console. Variables apply on the next deploy (`upsun redeploy`).

## First run

1. Push: `git push upsun main`.
2. Open the setup link from the log: `upsun log app | grep "needs setup"`. It holds a one-time token and changes on every restart until setup is done.
3. Create the admin account. The sign-in link is emailed, so mail must work (the relay or your SMTP).

## Operations

```bash
upsun ssh -- deploy/upsun/kmdn doctor
upsun ssh -- deploy/upsun/kmdn backup --skip-db --out data/backups/kmdn.tar.zst
```

Back up the database with Upsun's own backups (`upsun backup:create`), which include the mount.
