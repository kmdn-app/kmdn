# 06 · Git and forges

## Forge adapters

```go
type Adapter interface {
    Kind() string                                   // "github" | "gitlab" | "git"
    Credential(ctx, repo) (*gitmirror.Credential, error)
    RepoInfo(ctx, repo) (RepoInfo, error)           // default branch, visibility, size
    BranchProtection(ctx, repo, branch) (Protection, error)
    ParseWebhook(r, body, secret) (Event, error)
}

// Forges with pull/merge requests (GitHub, GitLab; not plain git).
type ChangeRequester interface {
    OpenChangeRequest(ctx, repo, ChangeRequestInput{Head, Base, Title, Body, Draft}) (ChangeRequest, error)
    FindChangeRequest(ctx, repo, head) (ChangeRequest, bool, error)
    SetDraft(ctx, repo, cr, draft bool) error      // GitHub: GraphQL; GitLab: "Draft:" title prefix
    MergeChangeRequest(ctx, repo, cr, MergeInput{Title, Message, HeadSHA}) (MergeResult, error)
}
```

### GitHub (github.com and GitHub Enterprise Server)

- One **GitHub App** per kmdn instance, created from a manifest during setup (the setup wizard redirects to GitHub's app-manifest flow, then stores App ID, private key, webhook secret, client ID/secret). GHES: admin enters the base URL first.
- **Permissions**: Contents: read & write · Metadata: read · Pull requests: read & write (every revision has one) · Administration: read (branch protection / rulesets detection). User permission: email addresses read (for noreply/linked email).
- **Events**: `push`, `pull_request`, `installation`, `installation_repositories`, `repository` (rename/transfer/delete).
- **Tokens**: installation access tokens (1 h), cached and refreshed at 50 min.
- **User OAuth**: the same App's user-to-server OAuth for "Continue with GitHub" and account linking.

### GitLab (gitlab.com and self-managed)

- Optionally, the admin registers a **GitLab OAuth application** (per GitLab host) for sign-in and account linking: confidential, scope `read_user`, redirect URI `<base_url>/api/v1/auth/oauth/callback` (one URL for every GitLab host; the OAuth state names the host). Admin → Forges → Add GitLab shows it. Without one, repositories still connect with access tokens; people just can't sign in with GitLab or link their GitLab identity.
- Repo access: a **Project or Group Access Token** with `api` + `write_repository`, role Developer (Maintainer if pushing to protected branches is intended). Two ways to provide it:
  1. Repo admin pastes a token created in GitLab.
  2. Repo admin with a linked GitLab account that is Maintainer on the project/group clicks "Create token automatically"; kmdn calls the access-token API with the admin's OAuth token, stores the result, and never keeps the admin's token.
- Token expiry is tracked; kmdn rotates via the self-rotate API 7 days before expiry and alerts admins if rotation fails.
- Webhooks (push, merge request) are registered automatically with a secret token.

## Mirrors

- Location: `<data>/mirrors/<forge>/<host>/<owner>/<repo>.git` (bare).
- kmdn keeps each open revision's branch tip at `refs/kmdn/revisions/<revision_id>` so its commits survive gc; a mirror that lost it (restored, re-cloned) fetches the branch back.
- Initial clone: `git clone --bare --filter=blob:none` then fetch blobs for the content root on demand, keeping large non-content repos cheap. Only the target branch is fetched (`+refs/heads/<target>:refs/heads/<target>`), plus `refs/kmdn/*` for kmdn's own refs.
- Update triggers: push webhook (primary), periodic fetch every 5 min as a safety net, manual "Refresh" in repo settings.
- Credentials are provided per command through a `GIT_ASKPASS` helper reading from an in-memory pipe; never written to disk or remotes.
- Reads: tree listing, blob reads (`git cat-file --batch` long-running process per repo), history (`git log --first-parent --follow -- <path>`: a page's published versions are the target branch's own commits, so each publish is one version while the commits saved on the revision branch stay in history and blame), blame (`git blame --porcelain`), diffs.
- `git` CLI ≥ 2.40 required (for `merge-tree --write-tree`). `kmdn doctor` checks it.

## Revision branches

Every revision is a branch on the forge with a pull/merge request, so the forge shows each change as it's written and publishing is a merge ([D58](decisions.md), [D59](decisions.md)). Reviews, comments and approvals still happen in kmdn.

- **Start.** Creating a revision queues a job that pushes `kmdn/<number>-<slug>` (slug from the title, 40 characters; `-2`, `-3`… if the name is taken by someone else) from `base_sha`, starting with an empty commit "Start revision #N: <title>" authored by the revision's creator and carrying the `Kmdn-Revision:` trailer (GitHub refuses a pull request without commits). It then opens a **draft** pull request (GitHub) or a merge request titled `Draft: <title>` (GitLab) against the target branch, whose description links back to the revision. Repositories without draft pull requests (GitHub private repositories on free plans) get a regular one. Plain git remotes get the branch only. The job is idempotent: it adopts a branch whose history carries the revision's trailer and a pull request already open from the branch.
- **Draft ↔ ready.** The pull request is a draft while the revision is Editing and ready for review while In review or Approved: Submit for review marks it ready; Withdraw, Request changes and conflicts from updates turn it back into a draft. GitHub needs GraphQL for both (`markPullRequestReadyForReview`, `convertPullRequestToDraft`); GitLab toggles the title prefix.
- **Commits.** Each **Save all** writes one commit on the branch (see [05](05-collaboration.md#saving-and-checkpoints)). The commit's tree is the revision's base with its manifest applied, its parent is the branch tip, and it's pushed with a lease on the tip kmdn last pushed. When `base_sha` moved since the branch last took it (updates from Published, or a fast-forward), the commit also has the new base as a second parent, so the pull request's diff only shows the revision's own changes.
- **Pushes from elsewhere.** kmdn owns the branch. If the lease fails because someone pushed to it, saving stops with "The branch changed outside kmdn"; v1 doesn't import those commits.
- **Forge events.** A pull request merged on the forge publishes the revision with the merge commit, whatever its state. One closed without merging is noted in the revision's activity (and stops a publish in progress).
- **State stored** on `revisions`: `branch`, `branch_sha` (tip last pushed), `branch_base_sha` (base the branch last merged), `change_request_url/ref/node`, `change_request_draft`.

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
- Pending changes are saved first (a commit, like Save all, by the person applying), then kmdn applies `diff(ours, merged)` to each Y.Doc as one system transaction attributed to `kmdn-sync` on behalf of the person who applied it (never credited as authorship). If collaborators edited in the meantime, the diff is recomputed against the current state before applying.
- Clean hunks go in directly. Conflicted regions become `conflict` nodes holding both versions (a Published side and a This revision side). Until resolved, the page materializes with the revision's side.
- Blocks the merge doesn't change keep their pending suggestions; blocks it rewrites (or a paragraph with a pending split) lose them, as if rejected.
- `base_sha = H`, `base_md` updated. Activity: "Tom applied updates from Published (2 pages, 1 conflict)".
- Content changed, so approvals reset (see [07](07-review.md#who-can-do-what-by-state)).
- If the update brought conflicts into an In review or Approved revision, it returns to **Editing** with `has_conflicts`.

"Not now" dismisses the banner for that person for 4 hours; the block on Approve/Publish stays.

### Conflict resolution

Conflict nodes render inline in the normal editor as a card: **Published** (theirs) above **This revision** (ours), both editable, with **Keep Published** / **Keep this revision** / **Keep both** (both versions one after the other, to merge by hand). Resolving is a direct edit even while Suggesting is on. To agree on a choice, editors comment on the conflict like on any passage ("Tom picked Keep this revision · Priya 👍"). Any editor can resolve. When a page materializes without conflict blocks, its `has_conflicts` clears; when no page has one left, the revision's clears (activity: "All conflicts are resolved") and an editor resubmits to the same reviewers. Undoing a resolution brings the conflict and the flag back. The assistant can explain a conflict or propose a merged version as a suggestion.

A page Published deleted while the revision edits it shows a banner instead: **Keep this revision's page** (publishing adds it back) or **Delete it too** (it leaves the revision).

## Publishing

Preconditions: revision Approved, no pending suggestions, no conflicts, no pending update from Published. If Published moved after approval and touches the revision's files, a pending update appears and publishing waits for it (which resets approvals). If Published moved but touches none of the revision's files, the base fast-forwards and publishing proceeds.

Publishing **merges the revision's pull request** with a merge commit, whether the target branch is protected or not ([D60](decisions.md)). The commits people saved stay in the target branch's history.

1. Save pending changes: if the content differs from the last commit, a final commit is saved on the branch (authored by the person publishing).
2. Build the merge message ([Attribution](#attribution)).
3. Mark the pull request ready if it's still a draft.
4. Merge it through the forge API with the merge method **merge** (never squash or rebase), with the message as the merge commit's title and body, pinned to the branch tip kmdn pushed:
   - **GitHub**: `PUT /repos/{o}/{r}/pulls/{n}/merge` with `merge_method: merge`. GitHub makes and signs the merge commit.
   - **GitLab**: `PUT /projects/{id}/merge_requests/{iid}/merge` with `merge_commit_message` and `sha`.
   - **Plain git**: kmdn writes the merge commit in the mirror (parents: target head, branch tip) and pushes it with a lease on the head.
5. **Protected branches.** kmdn merges through the API, so protection that only restricts pushes doesn't stop it. When the forge refuses because required checks are still running, kmdn turns on auto-merge (GitHub auto-merge with the merge method *merge*; GitLab "merge when pipeline succeeds") and the revision stays **Publishing** until the merge webhook arrives. Protection that needs approvals on the forge can't be satisfied by kmdn (the pull request's author is kmdn's bot): the Publish dialog says to add the kmdn App (or the token's bot user) to the rule's bypass list. A repository that doesn't allow merge commits can't be published to; the Publish dialog says so.
6. After the merge: fetch the mirror, mark the revision Published with the merge commit's SHA, record an audit entry, notify, fire outgoing webhooks, close rooms (read-only), delete the revision branch on the forge.

Idempotency: publish is a job with a stable key per revision; a crash mid-publish resumes by checking whether the target branch already contains a commit with trailer `Kmdn-Revision: <revision url>` (the merge commit carries it), or the pull request is already merged.

## Attribution

The merge commit that publishes a revision (and, on plain git, the merge kmdn writes) has this message. The commits saved on the branch are authored by the person who clicked Save all (commit email below), committed by kmdn, and carry the `Kmdn-Revision:` trailer.

Merge commit format:

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
- Merge commit author: the forge's merging identity (`kmdn[bot]` on GitHub, the access token's bot user on GitLab, `kmdn` on plain git). Saved commits: author = the person, committer = kmdn.

## Webhook ingress

`POST /hooks/github` and `POST /hooks/gitlab/<connection_id>`. Signature verified (HMAC SHA-256 / secret token). Events are stored in `webhook_deliveries` (dedup by delivery ID) and processed by jobs. Unknown repos are ignored and logged.

## Failure handling

- Forge down: publishing retries with backoff up to 1 h, revision shows "Publishing delayed". Editing is unaffected.
- Token revoked / App uninstalled: repo goes to "Disconnected" health state; revisions become read-only, admins alerted in inbox.
- Force-push on target branch (history rewritten): the update preparation treats the new head as theirs with `base_md` unchanged, which still works because merges are content-based. Activity notes "Published history was rewritten".
- Repo renamed/transferred: `repository` event updates the connection.
