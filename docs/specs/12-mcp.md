# 12 · MCP server

kmdn exposes a **read-only Model Context Protocol server** so external agents (Claude Code, Claude Desktop, support bots, internal agents) can search and read published documentation.

## Scope

- **Read-only, published content only.** No revisions, no comments or doc discussions, no checkpoints, no user data beyond public authorship in history.
- **Per-key repo scope**: a key sees the repos the admin selected, or all repos (including repos connected later) when "All repos" is chosen.
- Content is limited to each repo's content root and include/exclude globs, exactly like the UI.

## Transport and endpoint

- MCP **Streamable HTTP** transport at `POST/GET https://<host>/mcp` (spec revision current at implementation time; 2025-06-18 or later).
- Stateless mode: no server-initiated requests, no sampling, no elicitation. SSE used only for streaming long tool results.
- Implemented in Go with the official `modelcontextprotocol/go-sdk`.
- `Origin` validation for browser clients; CORS disabled by default.

## Authentication: agent keys

- Header `Authorization: Bearer kmdn_ak_<id>_<secret>`.
- Created by **instance admins only** in Admin console → Agent keys.
- Fields: name, description, repo scope (list or all), expiry (30 / 90 / 365 days / never; default 90), created by, created at, last used at + IP, status (active, revoked, expired).
- The secret is shown **once** at creation, with a copy button and a ready-to-paste client config:

```json
{
  "mcpServers": {
    "kmdn-handbook": {
      "type": "http",
      "url": "https://kmdn.northwind.dev/mcp",
      "headers": { "Authorization": "Bearer kmdn_ak_…" }
    }
  }
}
```

- Stored as `id` + SHA-256 of the secret (keys are high-entropy, so a fast hash is fine). Lookup by `id`, constant-time compare.
- Revocation is immediate. Expired keys return 401 with a clear message.
- Rate limit per key (default 120 requests/min, 10 concurrent). Every tool call is written to the audit log (key, tool, repo, path/query).
- OAuth 2.1 authorization for MCP (per-user delegated access) is post-v1; agent keys cover machine clients.

## Tools

| Tool | Input | Output |
|------|-------|--------|
| `list_repos` | — | Repos visible to the key: id, name, forge, content root, description |
| `search` | `query`, `repo?`, `path_prefix?`, `limit?` (≤ 50) | Hits: repo, path, title, heading, snippet, url |
| `list_tree` | `repo`, `path?`, `depth?` | Files and folders (markdown and assets) |
| `read_doc` | `repo`, `path`, `heading?`, `format?` (`markdown` \| `text`), `max_chars?` | Content at the published head, frontmatter as structured data, outline, commit sha, updated_at, web url |
| `get_outline` | `repo`, `path` | Heading tree with slugs |
| `get_history` | `repo`, `path`, `limit?` | Published versions: sha, date, message title, authors/co-authors (display names only) |
| `read_doc_at` | `repo`, `path`, `sha` | Content at a past published version |
| `get_links` | `repo`, `path` | Outbound and inbound internal links |

Tool annotations: `readOnlyHint: true`, `openWorldHint: false`.

## Resources

Each published doc is also exposed as an MCP resource: `kmdn://<repo>/<path>` (`text/markdown`), listable per repo with pagination, so clients that prefer resources can attach docs directly. No subscriptions in v1.

## Prompts

One prompt, `answer_from_docs(question, repo?)`, that instructs the client model to search, read and cite kmdn docs with web URLs.

## Admin console

Admin → **Agent keys**: table (name, scope chips, created by/at, expires, last used, status, Revoke), "Create agent key" dialog, one-time reveal state with copy + config snippet, per-key usage sparkline (calls/day) and a link to that key's audit entries.

## Not in v1

- Write tools (create revision, suggest edits) — possible later as a separate key capability, still routed through revisions and review.
- Revision or comment access.
- Per-user OAuth-delegated MCP sessions.
