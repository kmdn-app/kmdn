# Publish claims

Status: proposed

## Context

[Issue #190](https://github.com/kmdn-app/kmdn/issues/190) records the remaining late-edit race after the saved-snapshot fix in #169.

A publish job can pass its approval check while a reviewer edits the same revision. The branch lock covers saves and Git work, but live document changes and file operations can still reset approvals. Pinning the saved commit prevents unapproved bytes from reaching the target, yet the job can close a revision containing later accepted edits. Repeating the approval read leaves the same race after the last read.

## Decision

We will serialize each revision's publish claim with every operation that changes its content or manifest. Under that shared gate, we will flush accepted edits, save the candidate snapshot, verify its approval and conflicts, and enter Publishing for that exact saved commit before starting merge side effects.

Every mutation will check the current state while holding the same gate. Mutations after a successful claim will fail before changing the shared document, with an explicit unsaved response. Publishing will remain read-only while the external operation runs. Completion will finalize the claimed commit; failure and restart recovery will release or resume the claim without overwriting a newer state.

We will define one lock order before implementation and cover live updates, file operations, suggestion resolution, updates from Published, checkpoint restore and assistant changes. Save recovery will follow the journaled-save decision in ADR 0002.

## Consequences

The published commit and the accepted editable state have a defined boundary. A last-second edit either participates in the approval check or is refused, so a successful publish cannot silently strand it. Mutations and publishing now share a gate across several services. The implementation needs deterministic concurrency and recovery tests, including clients that obtained write permission before the state changed.
