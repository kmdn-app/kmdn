# 10 · Data model

IDs are prefixed ULIDs stored as text (`usr_01J…`, `rep_…`, `rev_…`) for readability in logs and URLs. Timestamps are UTC (`timestamptz` on Postgres, ISO-8601 text on SQLite via sqlc type overrides). Schema is kept portable: no Postgres-only types except in optional indexes; JSON columns are `jsonb` / `text` with JSON functions.

## Identity and access

```
users            id, email (unique, citext), name, avatar_asset_id, locale, theme,
                 commit_email_mode (forge_noreply|account|custom), commit_email_custom,
                 is_instance_admin, status (active|deactivated), created_at, last_active_at
user_emails      user_id, email, verified_at                      -- custom commit emails
passkeys         id, user_id, credential_id (unique), public_key, sign_count, transports,
                 name, created_at, last_used_at
linked_accounts  id, user_id, forge_host_id, forge_user_id, login, emails_json,
                 noreply_email, avatar_url, linked_at            -- unique(forge_host_id, forge_user_id)
sessions         id (hash), user_id, created_at, last_seen_at, expires_at, ip, user_agent
magic_links      token_hash, email, nonce_hash, expires_at, used_at, ip
invites          id, email, invited_by, repo_id?, role?, token_hash, expires_at, accepted_at
groups           id, name (unique), description
group_members    group_id, user_id
repo_members     repo_id, principal_type (user|group), principal_id, role (viewer|contributor|maintainer|admin)
agent_keys       id, name, description, secret_hash, all_repos bool, created_by,
                 created_at, expires_at, revoked_at, last_used_at, last_used_ip
agent_key_repos  agent_key_id, repo_id
```

## Forges and repos

```
forge_hosts      id, kind (github|gitlab), base_url, display_name,
                 app_id, app_slug, client_id, secrets_ref           -- GitHub App / GitLab OAuth app
forge_installs   id, forge_host_id, external_id, account_login      -- GitHub installations
repos            id, forge_host_id, install_id?, external_id, owner, name, display_name,
                 default_branch, target_branch, content_root, include_globs, exclude_globs,
                 assets_config_json, routes_json, settings_json,     -- allow_self_approval, assistant_enabled…
                 token_ref?,                                          -- GitLab access token
                 protection_json, protection_checked_at,
                 mirror_path, head_sha, health (ok|degraded|disconnected), health_detail,
                 kmdn_yml_sha, created_at
secrets          id, kind, ciphertext, data_key_ciphertext, created_at, rotated_at
```

## Revisions

```
revisions           id, repo_id, number (per repo, for URLs), slug, title, description,
                 state (editing|in_review|approved|publishing|published|closed),
                 changes_requested bool, has_conflicts bool, review_round int, base_sha, created_by, created_at, updated_at, submitted_at,
                 published_at, published_sha, change_request_url, change_request_ref,
                 closed_at, archived_at
revision_members    revision_id, user_id, role (owner|editor), invited_by, added_at
revision_files      id, revision_id, path, op (modify|add|delete|rename), from_path?,
                 base_md, base_blob_sha, content_md, content_hash, materialized_at,
                 has_conflicts bool, ydoc_id?
revision_assets     id, revision_id, path, upload_id, size, mime, sha256
revision_events     id, revision_id, actor_type (user|assistant|system), actor_id, kind, data_json, created_at
                 -- created, submitted, approved, changes_requested, synced, conflict, resolved,
                 -- file_added/renamed/deleted, published, closed, reopened, version_named
revision_checkpoints   id, revision_id, name?, kind (auto|named|pre_update|submit|pre_restore),
                 created_by?, created_at
revision_checkpoint_files version_id, path, ydoc_snapshot_id, content_md
approvals        id, revision_id, user_id, review_round, state (approved|changes_requested|dismissed),
                 content_hash, created_at, dismissed_at
revision_reviewers revision_id, user_id, requested_by, created_at, removed_at     -- assigned reviewers; all must approve
revision_updates   id, revision_id, from_sha, to_sha, status (pending|applied|superseded),
                   files_json,          -- per file: merged markdown ref, incoming hunks, conflicts
                   prepared_at, applied_by?, applied_at?
```

## Collaboration storage

```
ydocs            id, revision_id, path, engine_version, created_at
ydoc_snapshots   id, ydoc_id, state (blob), state_vector (blob), update_seq, created_at
ydoc_updates     ydoc_id, seq, update (blob), client_id, user_id, created_at    -- pk(ydoc_id, seq)
ydoc_clients     ydoc_id, client_id, user_id, kind (human|assistant|sync), on_behalf_of?
```

On SQLite, blobs live in the DB (WAL, `page_size=8192`). For very large installs, `storage.ydocs: fs` switches snapshots to `<data>/ydocs/…` files with DB pointers.

## Review and discussions

```
threads          id, repo_id, revision_id?, path, kind (revision|discussion), review_round?,
                 anchor_json,       -- revision: y relative positions ref; discussion: quote/prefix/suffix/sha
                 state (open|resolved|detached|outdated), created_by, created_at,
                 resolved_by, resolved_at, linked_revision_id?
comments         id, thread_id, author_id, body_md, created_at, edited_at, deleted_at
reactions        comment_id, user_id, kind
suggestions      id, revision_id, path, author_type (user|assistant), author_id, on_behalf_of?,
                 assistant_run_id?, status (pending|accepted|rejected), thread_id?,
                 created_at, decided_by, decided_at
```

## Assistant

```
assistant_threads  id, repo_id, revision_id? (unique when set), owner_id? (for private Q&A), created_at
assistant_messages id, thread_id, role (user|assistant|tool), author_id?, content_json, created_at
assistant_runs     id, thread_id, requested_by, provider, model, status, started_at, ended_at,
                   input_tokens, output_tokens, cache_read_tokens, tool_calls, error
```

## Search, links, history cache

```
search_docs      repo_id, scope (published|revision:<id>), path, title, headings, body, updated_at
passages         id, repo_id, scope, path, heading_slug, content_hash, text, tokens
passage_embeddings passage_id, model, dims, vector (blob float32)
consistency_findings id, repo_id, kind (contradiction|duplicate), passage_a, passage_b,
                 path_a, path_b, similarity, judgment_json, status (open|ignored|fixing|closed),
                 revision_id?, ignored_by?, ignore_reason?, first_seen_at, last_seen_at
consistency_scans  id, repo_id, started_at, finished_at, pairs_checked, llm_calls, findings, error
                 -- + FTS5 virtual table / tsvector index
links            repo_id, scope, from_path, to_path, anchor, kind (relative|route|external), line
file_history     repo_id, path, sha, parent_sha, date, title, author_json, coauthors_json, revision_id?
                 -- cache of git log, rebuilt incrementally on fetch
```

## Notifications, webhooks, audit, jobs

```
notifications    id, user_id, kind, repo_id?, revision_id?, thread_id?, actor_id?, data_json,
                 created_at, read_at
notification_prefs user_id, kind, in_app bool, push bool
push_subscriptions id, user_id, endpoint, p256dh, auth, user_agent, created_at, last_used_at
follows          user_id, repo_id, path, is_folder bool, auto_until?       -- pages or folders
page_reads       user_id, repo_id, path, last_read_sha, last_read_at
change_summaries repo_id, sha, path, summary, model                           -- "updated since your last visit"
outgoing_hooks   id, repo_id, kind (generic|slack), url, secret_ref, events[], active, created_by
hook_deliveries  id, hook_id, event, payload, status, attempts, last_error, next_attempt_at
webhook_deliveries id (forge delivery id), forge_host_id, event, received_at, processed_at, error
audit_log        id, at, actor_type (user|agent_key|system|assistant), actor_id, ip,
                 action, target_type, target_id, repo_id?, data_json
jobs             id, kind, key (unique while pending), payload, run_at, attempts, max_attempts,
                 locked_until, locked_by, last_error, created_at
uploads          id, sha256, size, mime, path_on_disk, created_by, created_at
```

## Data directory

```
<data>/
├─ kmdn.db (+ -wal, -shm)          when SQLite
├─ mirrors/<forge>/<host>/<owner>/<repo>.git
├─ uploads/<sha256[0:2]>/<sha256>   content-addressed
├─ ydocs/                            only with storage.ydocs: fs
└─ backups/
```

## Retention

- Closed revisions: Y.Doc updates compacted to a single snapshot after 90 days; revisions archived (read-only, still viewable).
- Published revisions: keep final snapshot and versions for 1 year (configurable), then compact to materialized markdown only.
- Audit log: 1 year default, configurable.
- Notifications: 90 days.
- Webhook deliveries: 30 days.
