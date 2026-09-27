# Configuration and CLI reference

kmdn reads its settings in this order, each layer overriding the previous one:

1. Built-in defaults
2. `kmdn.yaml` in the working directory, or the file given with `-config`
3. Environment variables

Every key has an environment variable: `KMDN_` plus the key's path in capitals, dots turned into underscores. `server.base_url` is `KMDN_SERVER_BASE_URL`. Lists take comma-separated values. `kmdn.yaml` can reference environment variables as `${NAME}`.

On Upsun you don't write a file. `deploy/upsun/kmdn` derives the main settings and you set the rest as `env:KMDN_…` variables. See [Install on Upsun](install-upsun.md#settings-upsun-provides).

kmdn checks the whole configuration at startup and lists every problem with a hint. An unknown key in `kmdn.yaml` is an error, which catches typos.

## `kmdn.yaml`

```yaml
server:
  base_url: https://docs.northwind.example
  listen: :8080
  trusted_proxies: [10.0.0.0/8]
data_dir: /data
secret_key: ${KMDN_SECRET_KEY}
db:
  url: sqlite:///data/kmdn.db       # or postgres://user:pass@host:5432/kmdn
smtp:
  host: smtp.example.com
  port: 587
  security: starttls
  username: kmdn
  password: ${KMDN_SMTP_PASSWORD}
  from: "Northwind Docs <docs@northwind.example>"
auth:
  auto_join_domains: []
  session_ttl: 720h
assistant:
  enabled: true
  provider: anthropic
  api_key: ${KMDN_ASSISTANT_API_KEY}
  base_url: ""
  model: ""
  review_model: ""
  short_model: ""
  embeddings:
    base_url: ""
    api_key: ""
    model: ""
limits:
  upload_max_mb: 10
  wysiwyg_max_file_mb: 1
telemetry:
  metrics: true
  metrics_token: ""
  otlp_endpoint: ""
  log_format: json
  log_level: info
hooks:
  allow_private: false
```

## Settings

### Server

| Key | Default | Description |
|---|---|---|
| `server.base_url` | `http://localhost:8080` | Where people reach kmdn. Used in sign-in links, webhooks, OAuth callbacks and the GitHub App. Passkeys are tied to its host, so changing the host invalidates them. |
| `server.listen` | `:8080` | Address and port to listen on. |
| `server.trusted_proxies` | none | Proxies whose `X-Forwarded-For` header kmdn trusts for client IPs, as CIDR ranges. |
| `data_dir` | `./data` | Git mirrors, uploads, the SQLite database and local backups. |
| `secret_key` | none, required | 32 random bytes, base64. Encrypts stored credentials. `kmdn init` generates one. |
| `db.url` | `sqlite://<data_dir>/kmdn.db` | `sqlite://path` or `postgres://…`. |

### Email

| Key | Default | Description |
|---|---|---|
| `smtp.host` | none | SMTP server. Setup can't finish without email. |
| `smtp.port` | `587` | |
| `smtp.security` | `starttls` | `starttls`, `tls` or `none`. `tls` usually goes with port 465. |
| `smtp.username`, `smtp.password` | none | |
| `smtp.from` | none | The sender, like `Northwind Docs <docs@northwind.example>`. |

SMTP can also be set in the admin console. Values from the file or the environment win and show as locked there.

### Sign-in

| Key | Default | Description |
|---|---|---|
| `auth.auto_join_domains` | none | Email domains whose people can create an account by signing in, without an invite. |
| `auth.session_ttl` | `720h` | How long a session lasts without use. |

### Assistant

| Key | Default | Description |
|---|---|---|
| `assistant.enabled` | `true` | Turns the assistant off entirely when `false`. |
| `assistant.provider` | none | `anthropic` or `openai`. When set, the provider settings come from here and show as locked in the admin console. |
| `assistant.api_key` | none | Required when `provider` is set. |
| `assistant.base_url` | the provider's API | For OpenAI-compatible servers and gateways. |
| `assistant.model` | the provider's default | Questions and edits. |
| `assistant.review_model` | the provider's default | Review summaries. |
| `assistant.short_model` | the provider's default | Titles, reader summaries, consistency judgments. |
| `assistant.embeddings.model` | none | Turns the consistency check on. |
| `assistant.embeddings.api_key` | none | |
| `assistant.embeddings.base_url` | `https://api.openai.com/v1` | Any OpenAI-compatible embeddings endpoint. |

Budgets and consistency scan settings live in the admin console. See [Assistant and consistency](assistant.md).

### Limits

| Key | Default | Description |
|---|---|---|
| `limits.upload_max_mb` | `10` | Largest image people can upload. |
| `limits.wysiwyg_max_file_mb` | `1` | Pages larger than this open in the markdown editor only. |

### Telemetry

| Key | Default | Description |
|---|---|---|
| `telemetry.metrics` | `true`, `false` on Upsun | Serves Prometheus metrics at `/metrics`. |
| `telemetry.metrics_token` | none | Requires `Authorization: Bearer <token>` on `/metrics`. |
| `telemetry.otlp_endpoint` | none | OpenTelemetry collector for traces, like `http://otel-collector:4318`. |
| `telemetry.log_format` | `json` | `json` or `text`. |
| `telemetry.log_level` | `info` | `debug`, `info`, `warn` or `error`. |

### Webhooks

| Key | Default | Description |
|---|---|---|
| `hooks.allow_private` | `false` | Lets outgoing webhooks reach loopback and private network addresses. |

## CLI

```
kmdn serve   [-config kmdn.yaml]   run the server
kmdn init    [-config kmdn.yaml]   write a config skeleton with a new secret key
kmdn migrate [status|up]           show or apply database migrations
kmdn admin rotate-secret-key -new KEY
                                   re-encrypt stored credentials with a new key
kmdn doctor  [-offline]            check git, the data dir, the database, the
                                   secret key, SMTP, forges and the AI provider
kmdn backup  [-out FILE.tar.zst] [-skip-db] [-include-secrets]
                                   back up the database, uploads and config
                                   (safe while the server runs)
kmdn restore -in FILE [-force]     restore a backup (stop the server first)
kmdn version                       print version information
```

| Command | Notes |
|---|---|
| `serve` | Runs pending migrations first. |
| `init` | Refuses to overwrite an existing file unless you add `-force`. |
| `migrate` | `status` lists each migration and whether it's applied. `up`, the default, applies pending ones. |
| `doctor` | Exits non-zero when a check fails. `-offline` skips SMTP, forge and AI checks. |
| `backup` | `.tar.zst`, or `.tar.gz` if the file name says so. `-skip-db` for PostgreSQL. `-include-secrets` keeps the secret key and passwords in the config copy. |
| `restore` | Refuses while kmdn is running, or over an existing database without `-force`. Writes the config to `<data dir>/kmdn.yaml.restored`. |
| `admin rotate-secret-key` | Re-encrypts every stored secret. Update `secret_key` afterwards. |

On Upsun, prefix commands with `upsun ssh -- deploy/upsun/kmdn`.
