# Agent notes

Conventions for coding agents working on kmdn. The design lives in [`docs/specs/`](docs/specs/README.md); the decision log is [`docs/specs/decisions.md`](docs/specs/decisions.md).

## Git

- Git always stores the full history of validated changes. Every saved step of a revision is a commit on its branch, and publishing merges that branch with a merge commit: never squash, rebase away, or force-push over validated work. This applies to kmdn's own behaviour (revision branches and publish) and to how this repository is developed (stacked PRs merged with merge commits).
