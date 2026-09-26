# 03 · Architecture

## Runtime shape

One Go binary. No Node at runtime.

```
                ┌──────────────────────────── kmdn (Go) ───────────────────────────┐
 Browser ──HTTPS──▶ HTTP router (chi)                                               │
  SPA (embedded) │   ├─ /            static SPA (go:embed web/dist)                 │
                 │   ├─ /api/v1/*    REST (oapi-codegen strict server)              │
                 │   ├─ /ws          WebSocket: Yjs sync, awareness, events, agent  │
                 │   ├─ /mcp         read-only MCP server (agent keys)              │
                 │   ├─ /hooks/*     GitHub / GitLab webhooks                       │
                 │   └─ /metrics     Prometheus                                     │
                 │                                                                   │
                 │  Services:  auth · users · repos · revisions · collab · review ·     │
                 │             sync · publish · history · search · assistant ·       │
                 │             notify · webhooks-out · audit · assets                │
                 │                                                                   │
                 │  Doc engine host: pool of JS runtimes running doc-engine bundle   │
                 │  Git layer: bare mirrors via git CLI (+ go-git for reads)          │
                 │  Forge adapters: github (App), gitlab (OAuth + access token)       │
                 │  LLM providers: anthropic, openai-compatible                       │
                 │  Job runner: in-process queue backed by DB table                   │
                 └───────────────┬───────────────────────┬──────────────────────────┘
                                 │                       │
                        SQLite (WAL) / Postgres    Data dir: mirrors/, ydocs/, uploads/, backups/
```

### Why these choices

- **Static SPA in the binary**: one artifact to ship and run, trivial self-hosting. SSR buys little for an authenticated editor.
- **Server-side doc engine in an embedded JS runtime**: the WYSIWYG model and the fidelity serializer are TypeScript. Running the same bundle in Go keeps one serializer, lets the server be authoritative (merge, persist, materialize markdown, diff, auto-sync) even with nobody online. See [04](04-doc-engine.md).
- **Bare mirrors**: fast local reads (tree, history, blame, diff), one code path for both forges, cheap auto-sync. Writes to GitHub go through the Git Data API for signed commits ([06](06-git-and-forges.md)).
- **SQLite default**: zero-config install. Postgres for teams that want managed DB/backups. Same schema via migrations and `sqlc`.
- **Single node**: collab rooms, JS runtime pool and job queue live in-process. No Redis. Scaling out is a non-goal for v1; the code keeps rooms behind an interface so a pub/sub backend can be added later.

## Tech stack

### Backend (Go ≥ 1.25)

| Concern | Choice |
|---------|--------|
| HTTP | `net/http` + `chi` router |
| API | OpenAPI 3.1 spec (`api/openapi.yaml`) → `oapi-codegen` strict server |
| WebSocket | `github.com/coder/websocket` |
| DB | `database/sql` + `sqlc`; drivers `modernc.org/sqlite` (pure Go, keeps CGO off) and `pgx/v5`; migrations with `goose` |
| JS runtime | Spike: `goja` vs QuickJS on `wazero`. Decision by benchmark (see [14](14-roadmap.md)) |
| Git | git CLI (≥ 2.40) for clone/fetch/push/merge-tree/blame; `go-git` for in-process object reads where faster |
| GitHub | `google/go-github`, `bradleyfalzon/ghinstallation` for App tokens |
| GitLab | `gitlab.com/gitlab-org/api/client-go` |
| Auth | `go-webauthn/webauthn` for passkeys; `golang.org/x/oauth2` |
| Email | `wneessen/go-mail` |
| LLM | `anthropic-sdk-go`; `openai-go` for OpenAI-compatible endpoints |
| MCP | `modelcontextprotocol/go-sdk` (Streamable HTTP) |
| Search | SQLite FTS5 / Postgres `tsvector` behind a `search.Index` interface |
| Config | `koanf` (YAML + env) |
| Logs / metrics / traces | `log/slog` JSON, `prometheus/client_golang`, OpenTelemetry SDK (OTLP exporter, off by default) |
| Images | stdlib `image` + `golang.org/x/image` for decode/resize; optional WebP conversion needs a pure-Go encoder (spike, CGO stays off) |

### Frontend

| Concern | Choice |
|---------|--------|
| Build | Vite, TypeScript strict, pnpm |
| Routing | TanStack Router (file-based, type-safe search params) |
| Data | TanStack Query with hooks generated from OpenAPI (`@hey-api/openapi-ts` or `orval`, spike) |
| UI | shadcn/ui (New York), Tailwind v4, lucide, Geist fonts, Sonner |
| Editor | Tiptap 3 (ProseMirror) + `y-prosemirror`; CodeMirror 6 + `y-codemirror.next`-style binding for source mode (custom, see [04](04-doc-engine.md)) |
| CRDT | Yjs, `y-protocols` (sync, awareness) |
| Diagrams / math | Mermaid (lazy-loaded), KaTeX |
| i18n | i18next + ICU |
| Tests | Vitest, Testing Library, Playwright (E2E against a real binary with a fake forge) |

## Monorepo layout

```
kmdn/
├─ cmd/kmdn/                 main: serve, migrate, admin, backup, restore, doctor
├─ internal/
│  ├─ api/                   generated server + handlers
│  ├─ auth/                  magic links, passkeys, oauth, sessions
│  ├─ users/  groups/        accounts, groups, roles
│  ├─ repos/                 repo connection, settings, .kmdn.yml
│  ├─ forge/                 interface + github/, gitlab/
│  ├─ gitmirror/             bare mirrors, fetch, tree/blob reads, merge-tree, blame
│  ├─ revisions/                revision lifecycle, file ops, state machine
│  ├─ collab/                rooms, Yjs update log, awareness, WS hub
│  ├─ docengine/             JS runtime pool + typed bridge to doc-engine bundle
│  ├─ sync/                  updates from Published: prepare, preview, apply; conflicts
│  ├─ knowledge/             link graph, passage index, embeddings, consistency checks and scans
│  ├─ review/                comments, suggestions metadata, approvals
│  ├─ publish/               commit building, attribution, PR fallback
│  ├─ history/               file history, blame, checkpoints
│  ├─ search/                FTS indexing
│  ├─ assistant/             agent loop, tools, providers
│  ├─ mcp/                   read-only MCP server, agent keys
│  ├─ notify/                inbox, outgoing webhooks
│  ├─ assets/                uploads, image processing
│  ├─ audit/                 audit log
│  ├─ jobs/                  DB-backed job queue
│  ├─ store/                 sqlc queries, migrations (sqlite/, postgres/)
│  └─ config/  telemetry/  web/ (go:embed of web/dist)
├─ api/openapi.yaml
├─ web/                      TanStack Router SPA
├─ packages/
│  ├─ doc-engine/            TS: schema, md parse/serialize, diff, merge, link index
│  └─ api-client/            generated TS client + query hooks
├─ e2e/                      Playwright
├─ testdata/                 markdown fidelity corpus, fixture repos
├─ docs/specs/  docs/mockups/
├─ Makefile  Dockerfile  go.mod  pnpm-workspace.yaml
└─ LICENSE (AGPL-3.0)
```

## Build

1. `pnpm -r build` → `packages/doc-engine/dist/engine.js` (IIFE bundle for the Go host, target ES2020, no DOM) and `web/dist`.
2. `go generate ./...` → API server stubs, sqlc, embeds engine bundle and SPA.
3. `go build -trimpath -ldflags "-s -w -X main.version=…" ./cmd/kmdn` with `CGO_ENABLED=0`.
4. GoReleaser: linux/darwin × amd64/arm64 binaries, multi-arch Docker image (distroless-ish base + git).

`make dev` runs Vite dev server (proxying `/api`, `/ws`) and `air` for the Go server.

## Concurrency model

- **Rooms**: one `collab.Room` per open revision file (`revision_id`, `path`). Holds the Y.Doc in a JS runtime from the pool (pinned while the room is active), connected clients, awareness state. Room is loaded from snapshot + update log on first join, evicted after 5 min idle (snapshot flushed).
- **JS runtime pool**: N runtimes (default = CPU count). Pure functions (parse, serialize, diff) borrow any runtime; rooms pin one runtime per active doc up to a cap, beyond which update application is serialized through a shared runtime. Budget/timeout per call (default 2s) to contain pathological inputs.
- **Jobs**: DB-backed queue (`jobs` table, `SELECT … FOR UPDATE SKIP LOCKED` on Postgres, single worker loop on SQLite). Job types: mirror fetch, prepare revision update, apply revision update, publish, embed passages, consistency check (revision) and scan (repo), change summary, web push, search index, webhook delivery, email send, assistant run, snapshot compaction, link check.
- **Per-repo lock** serializes mirror mutations (fetch, publish) to avoid ref races.
