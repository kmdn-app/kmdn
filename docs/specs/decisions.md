# Decision log

Decisions from the design interview (D1–D45, 2026-09-26) and the first mockup review (D46–D56, 2026-09-27). Each links to the spec that details it.

| # | Topic | Decision | Alternatives considered | Spec |
|---|-------|----------|-------------------------|------|
| D1 | Core model | Reviews happen **inside kmdn**. On approval kmdn writes to the target branch. Forge is storage (plus PR/MR only for protected branches, D24) | Revision = branch + forge PR; live main + optional review; per-repo configurable | [06](06-git-and-forges.md), [07](07-review.md) |
| D2 | Revision unit | **Multi-file changeset** | One doc per revision; per-doc pending layer | [05](05-collaboration.md) |
| D3 | Co-editing | **Live multiplayer CRDT (Yjs)** | Turn-based locks; single-author revisions | [05](05-collaboration.md) |
| D4 | Runtime shape | **Static SPA (TanStack Router) embedded in the Go binary**; no Node at runtime | Go supervising TanStack Start; two services | [03](03-architecture.md) |
| D5 | Editor | **WYSIWYG (Tiptap/ProseMirror) + source toggle**, block-reuse fidelity serializer | Obsidian-style live preview; normalize on import; BlockNote | [04](04-doc-engine.md) |
| D6 | Doc engine host | **Shared TS doc engine run in Go via an embedded JS runtime**, server authoritative | Go relays + clients serialize; yrs wasm + Go serializer | [04](04-doc-engine.md) |
| D7 | Markdown scope | **GFM core, YAML frontmatter (properties panel), Mermaid + KaTeX**; everything else raw blocks | Admonitions/callouts native (deferred) | [04](04-doc-engine.md) |
| D8 | Source mode | **Live bidirectional** | Source takes a file lock; source read-only | [04](04-doc-engine.md) |
| D9 | Commit shape | **One squashed commit** per published revision | Per-person commits; maintainer chooses | [06](06-git-and-forges.md) |
| D10 | Git access | **Bare mirror** for reads and tree building | Forge APIs only; hybrid | [06](06-git-and-forges.md) |
| D11 | Divergence | ~~Continuous auto-sync~~ → superseded by D52 | Rebase at approval; manual update | [06](06-git-and-forges.md) |
| D12 | Repo scope | **Configurable content root + globs + one target branch**, `.kmdn.yml` override (not for branch) | Whole repo any branch; multiple spaces | [06](06-git-and-forges.md) |
| D13 | User identity | **kmdn-owned accounts**, optional forge linking | Forge OAuth only; + OIDC | [09](09-auth-permissions.md) |
| D14 | Permissions | **kmdn-managed only**: Viewer / Contributor / Maintainer / Admin + groups | Forge-seeded; forge-mirrored | [09](09-auth-permissions.md) |
| D15 | Tenancy | **Single-tenant instance** | Workspaces; full SaaS | [01](01-product.md) |
| D16 | Storage | **SQLite default, Postgres optional, single node** | Postgres only; multi-node | [03](03-architecture.md), [10](10-data-model.md) |
| D17 | Review features | **Inline threads, suggestion mode, rendered + source diff**; approval rules deferred | CODEOWNERS-like rules | [07](07-review.md) |
| D18 | Lifecycle | ~~1 maintainer approval, editing open during review~~ → superseded by D48–D50. Still valid: self-approval off by default, explicit Publish | Approve = merge; freeze on submit | [07](07-review.md) |
| D19 | Revisions | **Git file history, revision checkpoints, blame overlay**; scheduled publish deferred | — | [05](05-collaboration.md), [11](11-api.md) |
| D20 | Assistant runtime | **Server-side Go loop, pluggable providers** (Anthropic default + OpenAI-compatible) | Anthropic only; Agent SDK sidecar | [08](08-assistant.md) |
| D21 | Assistant scope | **Repo Q&A with citations, edits as suggestions, multi-file revisions, review assistant** | — | [08](08-assistant.md) |
| D22 | Assistant threads | **One shared thread per revision**; outside revisions, private repo Q&A that hands off to a revision | Private shareable threads; both | [08](08-assistant.md) |
| D23 | Signing | **GitHub: commit via Git Data API (signed, Verified). GitLab: git push (unsigned)** | Plain push everywhere; machine-user signing key | [06](06-git-and-forges.md) |
| D24 | Branch protection | **Protected target → always open a PR/MR on publish**, tracked to merge | Bypass with PR fallback; require bypass | [06](06-git-and-forges.md) |
| D25 | Attribution | **Co-authored-by for surviving content contributors (forge noreply first), Reviewed-by approvers, Assisted-by for assistant** | Everyone who touched; no AI trailer | [06](06-git-and-forges.md) |
| D26 | GitLab connection | **OAuth app for users + Project/Group Access Token for repos**, gitlab.com + self-managed; GitHub.com + GHES | Bot user PAT; cloud only | [06](06-git-and-forges.md) |
| D27 | API | **REST + OpenAPI 3.1**, generated Go server and TS hooks, one multiplexed WebSocket | Connect-RPC; GraphQL | [11](11-api.md) |
| D28 | UI layout | **Docs-first** with Codex visual language | Codex 3-pane; chat-first | [02](02-ux.md) |
| D29 | Side panels | **Docked right panel** (Assistant / Comments / Changes / History / Links) + margin bubbles + ⌘K | Floating bubble; bottom composer | [02](02-ux.md) |
| D30 | Entering edit | ~~Context chip~~ → superseded by D47. Still valid: first keystroke on Published creates a revision | Explicit button; per-doc auto-revision | [02](02-ux.md) |
| D31 | Revision access | **Visible to repo, edit by invite**, maintainers always edit | Open to all contributors; private until submitted | [09](09-auth-permissions.md) |
| D32 | Assets | **Committed into the repo** with the revision | Object storage; configurable | [06](06-git-and-forges.md) |
| D33 | Notifications | **In-app inbox + outgoing webhooks/Slack**, no notification emails; extended by D54 | Email notifications | [13](13-operations.md) |
| D34 | SMTP | **Required** (magic links, invites) | Optional; none | [09](09-auth-permissions.md) |
| D35 | Sign-in | **Magic link + passkeys + forge OAuth**, no passwords | + passwords | [09](09-auth-permissions.md) |
| D36 | File ops | **Create/rename/move/delete, link rewriting on rename, broken link checker**; non-md editing deferred | — | [04](04-doc-engine.md) |
| D37 | SSG | **Generic + raw blocks**, `.mdx` source-only, route mapping in `.kmdn.yml` | SSG presets; MDX first-class | [04](04-doc-engine.md) |
| D38 | UX scope | **Light/dark, responsive read/comment/approve on mobile, i18n-ready**; offline deferred | Offline editing | [02](02-ux.md) |
| D39 | License | **AGPL-3.0** | Apache-2.0; MIT; FSL | [13](13-operations.md) |
| D40 | Ops | **Binary + Docker, admin console, audit log, Prometheus/OTel/JSON logs, backup/restore** | — | [13](13-operations.md) |
| D41 | Repo layout | **Go root + pnpm workspace** (`web/`, `packages/doc-engine`, `packages/api-client`) | Separate repos; Turborepo | [03](03-architecture.md) |
| D42 | Mockups | **Interactive HTML artifact**, copy in `docs/mockups/` | Figma; both | [02](02-ux.md) |
| D43 | Doc discussions | **Comments on published docs**, re-anchored by quote, "Fix this" → revision | Revisions only; page-level only | [07](07-review.md) |
| D44 | Spec location | **Markdown in repo** under `docs/specs/` | Repo + published doc; single SPEC.md | — |
| D45 | MCP server | **Read-only MCP (Streamable HTTP) at `/mcp`, admin-created agent keys, published content only**, scoped per repo | Revisions opt-in; instance-wide read | [12](12-mcp.md) |
| D46 | Naming | The changeset is called a **Revision**. Saved states inside it are **checkpoints**; a page's git history is **published versions**. "Revision history" is never used | Proposal; Change; Update | [01](01-product.md) |
| D47 | Sidebar | **Contextual sidebar with a revision picker**: Published context shows navigation, revisions and files; revision context shows changed files, people, checkpoints, all files | Top-bar context chip | [02](02-ux.md#contextual-sidebar) |
| D48 | Review UI | **Review happens in the editor**. Result view by default (deletions hidden), Changes and Source diff on demand. No per-file "viewed" checkbox, no separate file column | Separate diff screen | [07](07-review.md#result-and-changes-views) |
| D49 | Review rights | **Assigned reviewers edit directly; editors are read-only while In review** (comment, accept/reject reviewer suggestions, withdraw) | Reviewers suggest only; everyone keeps editing | [07](07-review.md#who-can-do-what-by-state) |
| D50 | Approval | **Every assigned reviewer must approve**; a reviewer's edit resets the other reviewers' approvals; reviewers are credited as co-authors | Any one reviewer; author OK on reviewer edits | [07](07-review.md) |
| D51 | Conflicts | **Conflicts send the revision back to Editing**; editors resolve inline (any editor, tracked, with a thread per conflict), then resubmit to the same reviewers | All editors sign off; owner decides | [06](06-git-and-forges.md#conflict-resolution) |
| D52 | Updates from Published | **Preview, then apply** (kmdn's rebase). Prepared automatically, applied by an editor (Editing) or assigned reviewer (In review/Approved); blocks Approve/Publish while pending; resets approvals | Auto-apply clean; auto while editing only | [06](06-git-and-forges.md#updates-from-published) |
| D53 | Comment order | **Hot topics** (default) or **Last updated** | Chronological only | [07](07-review.md#comments) |
| D54 | Notifications | **Participants in real time** (inbox + optional browser push) and **readers** ("updated since your last visit" banner with summary, follow pages/folders). Still no notification emails | What's new feed + Slack digest; email digests | [13](13-operations.md#notifications) |
| D55 | Consistency | **Duplicate/contradiction check on every revision + weekly repo scan**, embeddings for candidates + LLM judgment, advisory | Revisions only; scan only | [08](08-assistant.md#consistency-check-duplicates-and-contradictions) |
| D56 | Link graph | **Links tab with local graph per page + repo graph page** from the link index | Graph page only; backlinks lists only | [04](04-doc-engine.md#link-index-and-graph) |

## Defaults chosen without a dedicated question

Flagged so they can be challenged in review:

- Magic-link emails include a 6-digit code for cross-device sign-in.
- Auto-join by email domain exists, off by default.
- Agent keys scoped per repo (or all repos), default expiry 90 days.
- Default model `claude-sonnet-5`; per-task model routing.
- Pending suggestions block publishing; unresolved threads don't.
- Stale badge after 30 days; closed revisions archived after 90.
- Phones: no editing in v1.
- Commit trailer `Kmdn-Revision:` for idempotency and history linking.
- Reviewer suggestions based on who reviewed the touched folders in the last 6 months, falling back to all maintainers.
- Hot score: recency-weighted replies/reactions (6 h half-life-style decay).
- Push coalescing: one push per revision per 5 minutes, none while the revision is open and focused.
- Consistency thresholds: 0.78 candidate, 0.92 direct duplicate; scan cap 500 LLM calls; embeddings via an OpenAI-compatible endpoint (Anthropic has no embeddings API).
- "Changes requested" and "Conflict" are flags on Editing, not states.
