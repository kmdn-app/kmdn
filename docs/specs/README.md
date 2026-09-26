# kmdn specifications

kmdn is an open-source, self-hosted collaborative markdown editor: Google Docs for people, a git repository underneath. Anyone on the team can write and co-edit; repo maintainers review and publish. Every published change lands in GitHub or GitLab as a single commit made by the kmdn bot and co-signed by the people who wrote it.

Status: **draft specs, pre-implementation** (2026-09-26). Nothing here is built yet.

## Documents

| # | Document | Covers |
|---|----------|--------|
| 01 | [Product](01-product.md) | Vision, personas, glossary, v1 scope and non-goals |
| 02 | [UX and screens](02-ux.md) | Information architecture, app shell, every screen, key flows |
| 03 | [Architecture](03-architecture.md) | Runtime shape, components, repo layout, tech stack |
| 04 | [Document engine](04-doc-engine.md) | Schema, markdown fidelity, raw blocks, source mode, JS-in-Go, link index and graph |
| 05 | [Collaboration](05-collaboration.md) | Yjs, WebSocket protocol, presence, persistence, suggestions |
| 06 | [Git and forges](06-git-and-forges.md) | Mirrors, GitHub App, GitLab, updates from Published, conflicts, publishing, attribution |
| 07 | [Review](07-review.md) | Revision lifecycle, comments, suggestions, diffs, approvals, doc discussions |
| 08 | [Assistant](08-assistant.md) | Agent loop, tools, threads, providers, limits, consistency check |
| 09 | [Auth and permissions](09-auth-permissions.md) | Sign-in, sessions, roles, groups |
| 10 | [Data model](10-data-model.md) | Tables, storage layout |
| 11 | [API](11-api.md) | REST + OpenAPI, WebSocket channels, webhooks |
| 12 | [MCP server](12-mcp.md) | Read-only MCP endpoint for external agents, agent keys |
| 13 | [Operations](13-operations.md) | Config, distribution, admin console, audit, observability, backups, notifications |
| 14 | [Roadmap and risks](14-roadmap.md) | Spikes, milestones, risks, open questions |
| — | [Decision log](decisions.md) | Every decision taken in the design interview, with rationale |

UI mockups: [`docs/mockups/index.html`](../mockups/index.html).

## One-paragraph summary

A single Go binary serves a TanStack Router SPA (embedded with `go:embed`), a REST API described by OpenAPI, and one WebSocket per tab. Documents are edited in a Tiptap/ProseMirror WYSIWYG editor with a live Markdown source mode, co-edited through Yjs. A shared TypeScript **doc engine** (schema, markdown parser/serializer, diff) runs in the browser and inside Go through an embedded JS runtime, so the server can materialize byte-faithful markdown on its own. Changes are grouped into **revisions** (multi-file changesets). Maintainers review revisions inside kmdn with comments, suggestions and rendered diffs. On publish, kmdn produces one squashed commit on the target branch (GitHub via the Git Data API so it is signed and Verified; GitLab via `git push`), or opens a PR/MR when the branch is protected. Reviews happen in the editor: assigned reviewers edit directly while authors wait, and every reviewer approves. New Published changes are previewed and applied into open revisions by a person; conflicts send a revision back to its editors. A consistency check flags duplicate or contradicting passages, and a link graph shows how pages connect. A server-side assistant answers questions about the repo and proposes edits as tracked suggestions inside a revision's shared thread. External agents can read published docs through a read-only MCP endpoint using admin-issued agent keys. Storage is SQLite by default, Postgres optional, single node.
