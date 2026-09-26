# 01 · Product

## Problem

Teams keep documentation, handbooks, runbooks and content sites in git because git gives them history, review and deploy pipelines. The people who know the content best (HR, support, product, sales, ops) are locked out: they don't know markdown conventions, branches or pull requests. Engineers become copy-paste proxies. Meanwhile tools that non-technical people like (Google Docs, Notion) have no reviewable, versioned path into a repo.

kmdn closes that gap: a Docs-quality editor for everyone, with git as the source of truth and maintainers as gatekeepers.

## Goals

1. A non-technical person can find a page, fix it, and get it reviewed without ever seeing the words branch, commit or pull request.
2. A maintainer can review a multi-file change with the confidence of a code review: exact diff, discussion, suggestions, one-click publish.
3. The repo stays clean. Untouched markdown keeps its exact bytes. History shows one meaningful commit per change, credited to the right people.
4. Several people can edit the same revision at the same time, live.
5. An assistant helps people find answers in the repo and revision changes, always as reviewable suggestions.
6. One binary, self-hostable in minutes, open source (AGPL-3.0).

## Non-goals (v1)

- Multi-tenant SaaS: one instance is one organization. No workspaces, billing or quotas.
- Horizontal scaling: single node by design.
- Offline editing.
- MDX in WYSIWYG, SSG presets, site preview builds.
- Admonition/callout syntax in WYSIWYG (preserved as raw blocks).
- Scheduled publishing, approval rules beyond "every assigned reviewer approves", CODEOWNERS-style required reviewers.
- Editing non-markdown text files (yml, json) in v1.
- Email notifications (email is used for sign-in and invites only).
- Forge-synced permissions.

## Personas

| Persona | Example | Needs |
|---------|---------|-------|
| **Contributor, non-technical** | Tom, HR partner updating the onboarding handbook | Familiar editor, no git vocabulary, clear "who will review this", help from the assistant |
| **Contributor, technical** | Priya, engineer updating the deploy runbook | Source mode, exact markdown control, fast keyboard flows, clean diffs |
| **Maintainer** | Maya, docs lead | Review queue, exact diffs, suggestions instead of rewrites, confidence nothing breaks (links, frontmatter), attribution in git |
| **Viewer** | Luis, new hire | Read, search, ask the assistant, leave feedback on a page |
| **Instance admin** | Platform engineer | Easy install, forge connection, SMTP, LLM keys, user management, audit, backups |

## Glossary

| Term | Meaning | Shown to non-technical users as |
|------|---------|---------------------------------|
| **Instance** | One kmdn deployment | — |
| **Repo** | A connected GitHub/GitLab repository with a content root and a target branch | "Space" name = repo display name |
| **Content root** | Folder inside the repo exposed by kmdn (e.g. `docs/`) | Folder tree |
| **Target branch** | The branch revisions publish into (default branch unless configured) | "Published" |
| **Published version** | Content of the target branch at its current head | "Published" |
| **Revision** | A named, multi-file changeset based on the target branch, co-edited live, reviewed in kmdn, published as one commit | "Revision" |
| **Checkpoint** | Automatic or named point-in-time state of a revision (kmdn-internal) | "Checkpoint" |
| **Published version** | A past state of a page on the target branch (a git commit) | "Published version" |
| **Reviewer** | A maintainer assigned to a revision; all assigned reviewers must approve | "Reviewer" |
| **Suggestion** | A tracked insertion/deletion awaiting accept/reject | "Suggestion" |
| **Comment thread** | Discussion anchored to a range of text in a revision | "Comment" |
| **Doc discussion** | Comment thread anchored to a published doc (not in a revision) | "Comment" / "Feedback" |
| **Update from Published** | New target-branch commits prepared as a merge into an open revision, previewed, then applied by a person (kmdn's rebase) | "Updates from Published" |
| **Conflict** | A block where the revision and Published both changed; sends the revision back to Editing | "Conflict" |
| **Consistency finding** | Two passages in the repo that duplicate or contradict each other | "Duplicate" / "Contradiction" |
| **Publish** | Maintainer action that writes the approved revision to the target branch | "Publish" |
| **Assistant** | Server-side LLM agent | "Assistant" |

## v1 scope at a glance

- Accounts: magic link (SMTP required), passkeys, GitHub/GitLab OAuth linking. Instance admins, groups.
- Repos: GitHub App (github.com + GHES), GitLab (gitlab.com + self-managed) via OAuth app + access token. Content root, globs, target branch, `.kmdn.yml` override.
- Roles per repo: Viewer, Contributor, Maintainer, Admin. kmdn-managed only.
- Editor: Tiptap WYSIWYG + live bidirectional Markdown source mode. GFM, frontmatter properties panel, Mermaid, KaTeX, raw blocks for everything else. Byte-faithful round-trip.
- Live multiplayer (Yjs) with cursors and presence.
- Revisions: multi-file, create/rename/move/delete, images committed into the repo, link rewriting on rename, broken-link checker.
- Review in the editor: assigned reviewers edit directly while authors are read-only; Result view by default with a Changes/Source diff switch; inline threads sorted by hot topics; suggestion mode; every assigned reviewer approves; explicit Publish.
- Updates from Published: previewed, then applied; conflicts send the revision back to Editing and are resolved inline by its editors.
- Consistency check (duplicates, contradictions) per revision and as a weekly repo scan; link graph (per page and per repo).
- Publish: one squashed commit, Co-authored-by / Reviewed-by / Assisted-by trailers. PR/MR on protected branches.
- History: published versions from git, revision checkpoints, blame overlay.
- Doc discussions on published docs, "Fix this" → revision.
- Assistant: repo Q&A with citations, edits as suggestions, multi-file revisions, review assistant. Anthropic default, OpenAI-compatible endpoints.
- MCP: read-only endpoint for external agents (published content only), admin-created agent keys scoped per repo.
- Notifications: in-app inbox and optional browser push for participants; "updated since your last visit" banners and page/folder follows for readers; per-repo outgoing webhooks (generic + Slack). No notification emails.
- Light/dark, responsive read/comment/approve on mobile, i18n-ready (English only).
- Ops: single binary + Docker, admin console, audit log, Prometheus, OTel, JSON logs, backup/restore.
