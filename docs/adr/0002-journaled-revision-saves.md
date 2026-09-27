# Journaled revision saves

Status: accepted, 2026-09-27

## Context

[Issue #170](https://github.com/kmdn-app/kmdn/issues/170) reproduces a Git push that succeeds before the database update fails. The next save then mistakes kmdn's own commit for an external branch change. Git and the database cannot participate in one transaction, and validated history must never be rewritten.

## Decision

We will add an internal save-intent table that records the exact commit, previous branch tip, author, base, and immutable checkpoint content before pushing. A private Git ref keeps the prepared commit reachable. After pushing, one database transaction records the checkpoint and branch metadata and removes the intent.

On retry, we will finalize an intent only when the remote tip matches its exact commit, or retry that same push when the remote still matches its recorded previous tip. Any other tip remains an external-change conflict. Newer live edits remain unsaved until a separate save.

We will test interruption before and after push, transaction failure, process restart, checkpoint content, and true external pushes. The schema change is additive. Applying it to production is outside this request.

## Consequences

Retries can recover completed Git writes without force-push, squash, or rebase. Saves gain a durable preparation step and cleanup requirements. Backups include unfinished intents so recovery can continue after restart. No public API or intended user-facing save behavior changes.
