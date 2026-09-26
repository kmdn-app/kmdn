# 14 · Roadmap and risks

## Spikes (before M1, time-boxed)

| # | Spike | Question | Exit criteria |
|---|-------|----------|---------------|
| S1 | **JS runtime in Go** | goja vs QuickJS-on-wazero for the doc-engine bundle | Yjs apply-update, parse + serialize of a 200 KB markdown file, and 3-way merge run correctly in both; pick the faster one with acceptable memory (target: parse+serialize 200 KB < 150 ms, apply 1 update < 2 ms) |
| S2 | **Markdown fidelity** | Can a block-reuse serializer on micromark/mdast hit byte-exact round-trip and edit locality? | 100% round-trip on a 300-file corpus; single-block edits change only that block's bytes |
| S3 | **Live source mode** | Is bidirectional CodeMirror ↔ ProseMirror (via reparse + tree diff) usable with 2+ concurrent editors? | No cursor jumps in a scripted 3-client fuzz test over 5 min; latency < 400 ms |
| S4 | **Suggestion mode on y-prosemirror** | Tracked changes as marks + node-attr suggestions over Yjs without corrupting concurrent edits | Fuzz test with concurrent suggest/accept/reject converges on all clients |
| S5 | **GitHub signed commits** | Confirm App installation-token commits via Git Data API are signed and show Verified, including on GHES | Verified badge on github.com and GHES 3.x test instance |
| S6 | **GitLab token automation** | Create + self-rotate project/group access tokens via API from an admin's OAuth token, gitlab.com and self-managed | Scripted create/rotate works on gitlab.com and a GitLab CE container |
| S7 | **Codegen** | `@hey-api/openapi-ts` vs `orval` for TanStack Query hooks + discriminated event types | **Done:** `openapi-typescript` types + `openapi-fetch`, hooks written by hand over TanStack Query; server stays hand-written with a route/spec drift test |
| S8 | **Consistency quality** | Can embeddings + LLM judgment find real contradictions/duplicates without flooding maintainers? | On a seeded 300-page corpus with 20 planted contradictions: ≥ 80% found, ≤ 1 false positive per 50 pages |

## Milestones

### M0 · Foundations
Monorepo, Taskfile, CI, config, DB + migrations (SQLite/Postgres), setup wizard, magic link sign-in (SMTP), sessions, app shell (sidebar, top bar, right panel, ⌘K skeleton), theme, i18n plumbing, doc-engine package with parse/serialize + corpus tests, JS runtime host.

### M1 · Read
GitHub App (manifest flow) + GitLab connection, mirrors, webhooks, repo settings + `.kmdn.yml`, file tree, published doc rendering (GFM, frontmatter, Mermaid, KaTeX, raw blocks), search, file history, blame, roles/groups/invites, admin console (users, groups, repos).

### M2 · Revision and co-edit
Revisions (manifest, file ops, templates, assets), Yjs rooms + WS protocol + persistence, presence, WYSIWYG editing, source mode, checkpoints, link checker, link rewriting, contextual sidebar with revision picker.

### M3 · Review and publish
Comments (hot-topics sort), suggestion mode, review in the editor (assigned reviewers, per-state rights, Result/Changes/Source diff), lifecycle and approvals, updates from Published (prepare, preview, apply) + inline conflicts, publish (GitHub API commit, GitLab push, PR/MR fallback), attribution, inbox + web push, follows + "updated since your last visit", doc discussions + Fix this, outgoing webhooks/Slack, link graph (Links tab + graph page).

### M4 · Assistant and MCP
Providers (Anthropic, OpenAI-compatible), repo Q&A with citations, revision shared thread, edit tools as suggestions, propose_revision handoff, review assistant, usage limits; MCP server + agent keys. Passage index + embeddings, consistency check per revision and weekly repo scan, change summaries.

### M5 · Hardening → v1.0
Passkeys, mobile read/comment/approve, accessibility audit, audit log UI + export, observability, backup/restore, doctor, docs site (dogfooded in kmdn), load test (50 editors in a file, 500 open revisions), security review.

## Top risks

| Risk | Impact | Mitigation |
|------|--------|------------|
| Markdown fidelity edge cases produce noisy diffs | Core promise broken, maintainers lose trust | S2 corpus from day one, raw-block fallback for anything uncertain, "changed bytes" indicator in review showing untouched blocks stayed identical |
| JS-in-Go performance/compat | Server-side merge/materialize slow or broken | S1 benchmark, precompiled bundle, runtime pool, call budgets; fallback plan: materialize on clients with server validation |
| Source mode + CRDT cursor chaos | Power users frustrated | S3 fuzzing; don't patch the block under the active cursor; worst case, source mode takes a per-file soft lock (documented fallback) |
| Suggestion mode complexity on Yjs | Data corruption / divergent state | S4 fuzzing; restrict node-level suggestions in v1 to type/attr changes |
| Conflicts from Published confusing non-technical users | Stalled revisions | Block-aware merge reduces conflicts; preview before apply; plain-language inline resolver with a thread per conflict; assistant can explain one |
| Reviewers editing directly surprises authors | Trust, attribution disputes | Reviewer edits are live, attributed and in checkpoints; authors see them in Changes view; withdraw/request changes paths |
| Consistency findings are noisy or costly | Ignored feature, LLM bill | S8 thresholds, ignore memory by passage hash, per-scan LLM call cap, weekly default |
| Web Push setup friction | Notifications missed | Inbox remains source of truth; push is opt-in and optional |
| Branch protection everywhere | Every publish becomes a PR, undermining in-kmdn review | Setup explains the bypass option (rulesets bypass actor / GitLab "allowed to push" for the bot) as a recommended configuration; PR body shows kmdn approvals |
| GitLab token lifecycle | Silent breakage when tokens expire | Expiry tracking, auto-rotate, admin inbox alerts, health checks |
| LLM cost and data leakage | Surprise bills, content leaving the network | Budgets, per-repo disable, OpenAI-compatible local providers, audit |
| MCP key leakage | Published docs exfiltrated | Scope per repo, expiry by default, rate limits, last-used/IP visibility, instant revoke, audit |

## Open questions (to settle during specs review or spikes)

1. Should auto-join by email domain exist in v1 or only invites? (Spec says: exists, off by default.)
2. Revision URL format: `/r/<repo>/revisions/<number>` (per-repo counter) vs slug. (Spec says: number + slug.)
3. Repo display name and icon: from forge or editable in kmdn? (Spec says: editable, defaults to forge name.)
4. Do we allow a revision to span multiple repos? (Spec says: no.)
5. Callouts/admonitions native support timing (post-v1 per decision).
6. Personal API tokens for scripts: v1.x?
7. Should MCP later offer write tools (create revision / suggest), gated by a separate key capability?
