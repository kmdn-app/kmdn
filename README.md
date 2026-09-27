# kmdn

Collaborative markdown editing with a git backend.

kmdn gives anyone on a team a Google Docs-style editor over the markdown in a GitHub or GitLab repository. People co-edit **revisions** live, assigned maintainers review them in the same editor, and kmdn keeps each revision as a branch with a draft pull request: every **Save all** is a commit by the person who saved, and publishing merges the pull request, co-signed by the people who wrote it.

> **Status:** early development. The design is in [`docs/specs/`](docs/specs/README.md) and the UI mockups are in [`docs/mockups/`](docs/mockups/index.html). Work is tracked in [GitHub issues](https://github.com/kmdn-app/kmdn/issues) by milestone.

## What it does

- WYSIWYG editor with a live Markdown source mode, byte-faithful round-trips (untouched markdown keeps its exact bytes)
- Live multiplayer editing (Yjs) with presence
- Multi-file revisions, reviewed inside kmdn: comments, suggestions, result and diff views, approval by every assigned reviewer
- Updates from Published previewed and applied into open revisions; inline conflict resolution
- Each revision is a branch and a draft pull/merge request; Save all commits to it, publishing merges it with a merge commit carrying `Co-authored-by` trailers, protected branches included
- Server-side assistant: repo Q&A with citations, edits as tracked suggestions, review summaries, consistency checks
- Read-only MCP server for external agents
- Single Go binary with the web app embedded; SQLite by default, Postgres optional

## Install

With Docker (the image includes git; data lives in `/data`):

```bash
docker run -d -p 8080:8080 -v kmdn-data:/data \
  -e KMDN_SECRET_KEY="$(head -c32 /dev/urandom | base64)" \
  -e KMDN_SERVER_BASE_URL=http://localhost:8080 \
  ghcr.io/kmdn-app/kmdn:latest
```

Keep the secret key: stored credentials can't be decrypted without it. For Postgres or TLS with Caddy, see [`deploy/docker-compose.yml`](deploy/docker-compose.yml).

Or download a binary from the [releases](https://github.com/kmdn-app/kmdn/releases) (linux and macOS, amd64 and arm64; needs git ≥ 2.40 on PATH), then:

```bash
kmdn init        # writes kmdn.yaml with a new secret key
kmdn doctor      # checks git, the data dir, the database…
kmdn serve
```

Backups: `kmdn backup -out kmdn.tar.zst` (safe while running), `kmdn restore -in kmdn.tar.zst`.

## Repository layout

```
cmd/kmdn/            server entrypoint and CLI
internal/            Go packages (api, auth, collab, gitmirror, forge, revisions, …)
api/openapi.yaml     REST API contract
web/                 TanStack Router SPA (shadcn/ui)
packages/doc-engine  shared TypeScript document engine (browser + embedded in Go)
docs/specs/          specifications and decision log
docs/mockups/        clickable HTML mockups
deploy/              docker-compose example (Postgres, Caddy)
```

## Development

Requirements: Go ≥ 1.26, Node ≥ 22 with pnpm, git ≥ 2.40.

```bash
pnpm install
make dev        # Vite dev server + Go server with live reload
make test       # Go and TypeScript tests
make build      # single binary in ./bin/kmdn
```

## License

[AGPL-3.0](LICENSE)
