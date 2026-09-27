# 06 · Git and forges

## Forge adapters

```go
type Forge interface {
    Kind() string                                   // "github" | "gitlab"
    CloneURL(ctx, repo) (url string, auth Credential, err error)
    RepoInfo(ctx, repo) (RepoInfo, error)           // default branch, visibility, size
    BranchProtection(ctx, repo, branch) (Protection, error)
    PublishCommit(ctx, repo, req PublishRequest) (PublishResult, error)
    OpenChangeRequest(ctx, repo, req ChangeRequest) (ChangeRequestRef, error)
    ChangeRequestStatus(ctx, ref) (ChangeRequestState, error)
    VerifyWebhook(r *http.Request) (Event, error)
    OAuth() OAuthProvider                           // user linking / sign-in
}
```

### GitHub (github.com and GitHub Enterprise Server)

- One **GitHub App** per kmdn instance, created from a manifest during setup (the setup wizard redirects to GitHub's app-manifest flow, then stores App ID, private key, webhook secret, client ID/secret). GHES: admin enters the base URL first.
- **Permissions**: Contents: read & write · Metadata: read · Pull requests: read & write (protected-branch fallback) · Administration: read (branch protection / rulesets detection). User permission: email addresses read (for noreply/linked email).
- **Events**: `push`, `pull_request`, `installation`, `installation_repositories`, `repository` (rename/transfer/delete).
- **Tokens**: installation access tokens (1 h), cached and refreshed at 50 min.
- **User OAuth**: the same App's user-to-server OAuth for "Continue with GitHub" and account linking.

### GitLab (gitlab.com and self-managed)

- Admin registers a **GitLab OAuth application** (per GitLab host) for user linking/sign-in (`read_user`, `openid`, `email` scopes).
- Repo access: a **Project or Group Access Token** with `api` + `write_repository`, role Developer (Maintainer if pushing to protected branches is intended). Two ways to provide it:
  1. Repo admin pastes a token created in GitLab.
  2. Repo admin with a linked GitLab account that is Maintainer on the project/group clicks "Create token automatically"; kmdn calls the access-token API with the admin's OAuth token, stores the result, and never keeps the admin's token.
- Token expiry is tracked; kmdn rotates via the self-rotate API 7 days before expiry and alerts admins if rotation fails.
- Webhooks (push, merge request) are registered automatically with a secret token.

## Mirrors

- Location: `<data>/mirrors/<forge>/<host>/<owner>/<repo>.git` (bare).
- Initial clone: `git clone --bare --filter=blob:none` then fetch blobs for the content root on demand, keeping large non-content repos cheap. Only the target branch is fetched (`+refs/heads/<target>:refs/heads/<target>`), plus `refs/kmdn/*` for kmdn's own refs.
- Update triggers: push webhook (primary), periodic fetch every 5 min as a safety net, manual "Refresh" in repo settings.
- Credentials are provided per command through a `GIT_ASKPASS` helper reading from an in-memory pipe; never written to disk or remotes.
- Reads: tree listing, blob reads (`git cat-file --batch` long-running process per repo), history (`git log --follow -- <path>`), blame (`git blame --porcelain`), diffs.
- `git` CLI ≥ 2.40 required (for `merge-tree --write-tree`). `kmdn doctor` checks it.

## Repo connection settings

Stored in DB, optionally overridden by `.kmdn.yml` at the repo root on the target branch (repo settings show which values come from the file):

```yaml
# .kmdn.yml
root: docs/
include: ["**/*.md", "**/*.mdx", "**/*.{png,jpg,jpeg,gif,svg,webp}"]
exclude: ["docs/_generated/**"]
assets:
  path: "{dir}/images/{name}.{ext}"   # or "static/img/{name}.{ext}"
  maxSizeMB: 10
  convertToWebp: false
routes:                                # for link checker
  "/docs/": "docs/"
templates: .kmdn/templates/
```

The target branch is **not** overridable from `.kmdn.yml` (it would let a repo push change where kmdn writes).

## Updates from Published

This is kmdn's equivalent of rebasing a branch: bringing new Published commits into a revision that's in progress. It is **previewed, then applied** by a person. It never happens silently.

Each revision stores `base_sha` (the Published commit it last applied updates from). Each revision file stores `base_md` (its content at `base_sha`) alongside its live Y.Doc.

### 1. Detect and prepare (automatic)

On every target-branch update (webhook → fetch → new head `H`):

1. For each open revision, compute paths changed between `base_sha` and `H`, intersected with the revision's manifest. Paths the revision doesn't touch need nothing: the revision shows Published content for them anyway.
2. If the intersection is empty, fast-forward: `base_sha = H`, no user action, nothing shown.
3. Otherwise enqueue a **prepare-update job** per touched path:
   - `theirs = blob at H`, `base = base_md`, `ours = materialized revision markdown`.
   - Run `docengine.Merge3(base, ours, theirs)`: a block-aware 3-way merge (blocks aligned by content hash, then line-level diff3 inside a chunk both sides changed). Unlike git, changes to neighbouring blocks or lines merge; only changes to overlapping ranges (or two insertions at the same place) conflict.
   - Store the result as a **pending update** (`revision_updates` row): target sha, per-file merged markdown, list of incoming hunks, list of conflicts. Nothing touches the Y.Doc yet.
4. Structural cases are recorded in the pending update:
   - File deleted on Published but edited in the revision → conflict "Deleted on Published" (Keep this revision / Accept deletion).
   - File renamed on Published (git rename detection ≥ 50% similarity) → the revision's file follows the rename. *v1 doesn't detect renames yet: a rename shows as "Deleted on Published" for a page the revision edits.*
   - File created on Published at a path the revision also creates → conflict.
5. If Published moves again before the update is applied, the pending update is recomputed against the new head (one pending update per revision at a time).

### 2. Tell people

- Banner in the editor and review views, and a check on the revision overview: "Published changed 2 pages in this revision · Sam Lindqvist, 25 min ago · **Review and apply**".
- Inbox item for editors (while Editing) or assigned reviewers (while In review / Approved).
- While an update is pending, **Approve and Publish are blocked** ("Update from Published first"). Editing and commenting continue normally.

### 3. Preview

"Review and apply" opens the revision in the editor with incoming Published changes highlighted inline ("From Published · Sam, 25 min ago"), plus a summary card: pages changed, which merge cleanly, which conflict with the revision's own edits, and the note that applying resets approvals.

### 4. Apply

- Who: any revision editor while Editing; an assigned reviewer while In review or Approved.
- A checkpoint is recorded, then kmdn applies `diff(ours, merged)` to each Y.Doc as one system transaction attributed to `kmdn-sync` on behalf of the person who applied it (never credited as authorship). If collaborators edited in the meantime, the diff is recomputed against the current state before applying.
- Clean hunks go in directly. Conflicted regions become `conflict` nodes holding both versions (a Published side and a This revision side). Until resolved, the page materializes with the revision's side.
- Blocks the merge doesn't change keep their pending suggestions; blocks it rewrites (or a paragraph with a pending split) lose them, as if rejected.
- `base_sha = H`, `base_md` updated. Activity: "Tom applied updates from Published (2 pages, 1 conflict)".
- Content changed, so approvals reset (see [07](07-review.md#who-can-do-what-by-state)).
- If the update brought conflicts into an In review or Approved revision, it returns to **Editing** with `has_conflicts`.

"Not now" dismisses the banner for that person for 4 hours; the block on Approve/Publish stays.

### Conflict resolution

Conflict nodes render inline in the normal editor as a card: **Published** (theirs) vs **This revision** (ours) side by side, with Keep published / Keep this revision / Edit merged (both editable, prefilled with a naive merge). Each conflict has a small thread so the original editors can agree ("Tom picked Keep this revision · Priya 👍"). Any editor can resolve; the resolution is recorded as a tracked change visible to the others. When no conflict nodes remain, `has_conflicts` clears and an editor resubmits to the same reviewers. The assistant can explain a conflict or propose a merged version as a suggestion.

## Publishing

Preconditions: revision Approved, no pending suggestions, no conflicts, no pending update from Published. If Published moved after approval and touches the revision's files, a pending update appears and publishing waits for it (which resets approvals). If Published moved but touches none of the revision's files, the base fast-forwards and publishing proceeds.

1. Build the new tree: start from `H`'s tree, apply each manifest entry (modified content = final materialized markdown, added, deleted, renamed, assets from upload storage).
2. Build the commit message ([Attribution](#attribution)).
3. Check protection on the target branch (cached 5 min, re-checked at publish).
4. **Unprotected** branch:
   - **GitHub**: create blobs/tree/commit with the Git Data API (no `author`/`committer`, so GitHub signs it as `kmdn[bot]` → Verified), then `PATCH refs/heads/<target>` with `force: false`. A 422 non-fast-forward → re-sync and retry once.
   - **GitLab**: build the commit locally in the mirror (`git commit-tree` with author `kmdn <bot email>`), push with `--force-with-lease=refs/heads/<target>:<H>`. Unsigned.
5. **Protected** branch: push the same commit to `kmdn/<revision-slug>-<short-id>` and open a PR/MR titled with the revision title, body = revision description + review summary + link back to kmdn + list of approvers. Revision → **Publishing**. Watched via webhooks:
   - merged → **Published** (merge commit SHA stored; if squashed by the forge, the forge's squash commit is recorded).
   - closed unmerged → back to **Approved** with notice.
   - new commits pushed to the PR branch by someone else → revision notes "Changed on GitHub", not re-imported in v1.
6. After success: fetch mirror, mark revision Published, store `published_sha`, record audit entry, notify, fire outgoing webhooks, close rooms (read-only), delete the kmdn branch on the forge if one was created and merged.

Idempotency: publish is a job with a stable key per revision; a crash mid-publish resumes by checking whether the target ref already contains a commit with trailer `Kmdn-Revision: <revision_id>`.

## Attribution

Commit format:

```
Update onboarding for 2026

Refresh the first-week checklist, add the new laptop policy and
fix links to the IT portal.

Kmdn-Revision: https://kmdn.northwind.dev/r/handbook/revisions/d_8f3k2
Co-authored-by: Tom Okafor <12345+tokafor@users.noreply.github.com>
Co-authored-by: Priya Raman <priya@northwind.dev>
Reviewed-by: Maya Chen <maya@northwind.dev>
Assisted-by: kmdn-assistant
```

- **Title/body**: revision title and description; the review assistant proposes a summary the maintainer can edit in the Publish dialog. Title ≤ 72 chars, body wrapped at 72.
- **Co-authored-by**: every user whose inserted content survives in the final diff, or whose suggestion was accepted. Commenters are not co-authors. Order by amount of surviving content.
- **Email per user** (profile setting "Commit email"):
  1. linked forge noreply address for this repo's forge (default when linked; GitHub `<id>+<login>@users.noreply.github.com`, GitLab `<id>-<username>@users.noreply.<host>`),
  2. kmdn account email,
  3. custom verified email.
- **Reviewed-by**: approving maintainers.
- **Assisted-by: kmdn-assistant**: present when any surviving content came from the assistant. Assistant content is credited to the user who asked for it as Co-authored-by.
- **Kmdn-Revision**: link back to the revision (used for idempotency and for History to link commits to revisions).
- Author/committer: `kmdn[bot]` on GitHub (App identity), the access token's bot user on GitLab.

## Webhook ingress

`POST /hooks/github` and `POST /hooks/gitlab/<connection_id>`. Signature verified (HMAC SHA-256 / secret token). Events are stored in `webhook_deliveries` (dedup by delivery ID) and processed by jobs. Unknown repos are ignored and logged.

## Failure handling

- Forge down: publishing retries with backoff up to 1 h, revision shows "Publishing delayed". Editing is unaffected.
- Token revoked / App uninstalled: repo goes to "Disconnected" health state; revisions become read-only, admins alerted in inbox.
- Force-push on target branch (history rewritten): the update preparation treats the new head as theirs with `base_md` unchanged, which still works because merges are content-based. Activity notes "Published history was rewritten".
- Repo renamed/transferred: `repository` event updates the connection.
