# kmdn

Collaborative markdown editing with a git backend.

kmdn gives anyone on a team a Google Docs-style editor over the markdown in a GitHub or GitLab repository. People co-edit **revisions** live, assigned maintainers review them in the same editor, and kmdn publishes each approved revision as a single commit by the kmdn bot, co-signed by the people who wrote it.

> **Status:** early development. The design is in [`docs/specs/`](docs/specs/README.md) and the UI mockups are in [`docs/mockups/`](docs/mockups/index.html). Work is tracked in [GitHub issues](https://github.com/kmdn-app/kmdn/issues) by milestone.

## What it does

- WYSIWYG editor with a live Markdown source mode, byte-faithful round-trips (untouched markdown keeps its exact bytes)
- Live multiplayer editing (Yjs) with presence
- Multi-file revisions, reviewed inside kmdn: comments, suggestions, result and diff views, approval by every assigned reviewer
- Updates from Published previewed and applied into open revisions; inline conflict resolution
- One squashed commit per published revision with `Co-authored-by` trailers (signed and Verified on GitHub)
- Server-side assistant: repo Q&A with citations, edits as tracked suggestions, review summaries, consistency checks
- Read-only MCP server for external agents
- Single Go binary with the web app embedded; SQLite by default, Postgres optional

## Repository layout

```
cmd/kmdn/            server entrypoint and CLI
internal/            Go packages (api, auth, collab, gitmirror, forge, revisions, …)
api/openapi.yaml     REST API contract
web/                 TanStack Router SPA (shadcn/ui)
packages/doc-engine  shared TypeScript document engine (browser + embedded in Go)
docs/specs/          specifications and decision log
docs/mockups/        clickable HTML mockups
```

## Development

Requirements: Go ≥ 1.25, Node ≥ 22 with pnpm, git ≥ 2.40.

```bash
pnpm install
make dev        # Vite dev server + Go server with live reload
make test       # Go and TypeScript tests
make build      # single binary in ./bin/kmdn
```

## License

[AGPL-3.0](LICENSE)
