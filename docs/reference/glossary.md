# Glossary

kmdn keeps git vocabulary out of the way. The words on screen are the ones in the first column. When you need to know what happens in the repository, the last column gives the git term behind each one.

| Term | What it means | In git |
|------|---------------|--------|
| Instance | One kmdn deployment, used by one organization. | |
| Forge | The service that hosts your repositories: GitHub, GitHub Enterprise Server, GitLab or GitLab self-managed. kmdn also accepts any git URL. | The git host |
| Repository | A GitHub or GitLab repository connected to kmdn. The sidebar's repository switcher lists the ones you can see. | A repository |
| Content root | The folder kmdn shows, for example `docs/`. Files outside it don't appear. | A path prefix |
| Include and exclude | Glob patterns that narrow which files under the content root appear. | Path globs |
| Published | What readers see: the content of the target branch right now. | The head of the target branch |
| Target branch | The branch that holds Published and that revisions merge into. It's the repository's default branch unless an admin picks another. | Usually `main` |
| Page | A markdown file under the content root. | A `.md` file |
| Revision | A named set of changes to one or more pages. People edit it together, maintainers review it, and publishing merges it. | A branch `kmdn/<number>-<slug>` with a pull or merge request |
| Editor of a revision | The person who started it, plus anyone they added. Editors can change it while it's in Editing. | |
| Save all | Writes the revision's current content to its branch. Each click is one commit by the person who clicked. | A commit on the revision branch |
| Checkpoint | A saved state of a revision, one per Save all. You can view or restore it. | A commit on the revision branch |
| Unsaved changes | The revision differs from its last checkpoint. Live edits are synced between people, but not committed yet. | Uncommitted work |
| Suggestion | An insertion or deletion someone proposes instead of making directly. An editor accepts or rejects it. | |
| Comment thread | A discussion attached to a passage in a revision. | |
| Feedback, or doc discussion | A comment thread on a published page, outside any revision. | |
| Reviewer | A maintainer assigned to review a revision. Every assigned reviewer must approve. | Reviewers named in the merge commit's `Reviewed-by` trailer |
| Submit for review | Hands the revision to its reviewers. Editors become read-only until it comes back. | Marks the pull request ready for review |
| Approve | A reviewer's sign-off. Any later change to the content resets the other reviewers' approvals. | |
| Request changes | A reviewer sends the revision back to Editing with a note. | Turns the pull request back into a draft |
| Publish | A maintainer merges an approved revision into Published. | Merges the pull request with a merge commit |
| Published version | A past state of a page on Published. The History tab lists them. | A commit on the target branch |
| Updates from Published | New changes on Published that touch pages in an open revision. Someone previews them, then applies them. | Merging the target branch into the revision branch |
| Conflict | A passage that both the revision and Published changed. It sends the revision back to Editing until an editor picks a version. | A merge conflict |
| Assistant | kmdn's built-in AI helper. It answers questions with sources and proposes edits as suggestions. | |
| Consistency finding | Two passages in the repository that contradict each other or say the same thing twice. | |
| Link graph | A map of which pages link to which. | |
| Follow | Get an inbox item when a revision that changes a page, or anything in a folder, is published. | |
| Inbox | Your notifications inside kmdn. There are no notification emails. | |
| Agent key | A read-only key an admin creates so an external AI agent can read published pages over MCP. | |
| MCP | Model Context Protocol, the standard kmdn uses to let external agents search and read published docs. | |

## Roles

A role is set per repository. Someone can be a Maintainer in one repository and a Viewer in another. The table in [People and access](../admin/people.md#roles) lists what each role can do.

| Role | In one line |
|------|-------------|
| Viewer | Reads, searches, asks the assistant, comments. |
| Contributor | Starts revisions and edits the ones they're invited to. |
| Maintainer | Edits any revision in Editing, reviews, approves and publishes. |
| Admin | Changes the repository's settings and members. |
| Instance admin | Admin on every repository, plus the admin console. |

## Revision states

| State | Meaning |
|-------|---------|
| Editing | Its editors are changing it. Two flags can show next to it: **Changes requested** and **Conflict**. |
| In review | Waiting for its reviewers, who edit it directly if they need to. Editors can comment. |
| Approved | Every reviewer approved. A maintainer can publish it. |
| Publishing | kmdn asked the forge to merge it and is waiting, usually for required checks. |
| Published | Merged into the target branch. Read-only. |
| Closed | Abandoned. Its editors or a maintainer can reopen it within 90 days. |

The [revision lifecycle](git.md#revision-lifecycle) diagram shows how a revision moves between states.
