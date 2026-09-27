# Contributing to kmdn

Thanks for helping. kmdn is maintained by nlsio LLC and published under the [AGPL-3.0](LICENSE).

## Contributor License Agreement

Before your first pull request can be merged, you accept the [Contributor License Agreement](CLA.md). The CLA bot comments on the pull request with the sentence to post; you do it once for all kmdn repositories. The CLA lets nlsio LLC distribute your contribution under other terms as well as the AGPL (for example in its hosted service), while you keep your copyright and kmdn stays open source.

## Before you start

- Read the design in [`docs/specs/`](docs/specs/README.md) and the decision log in [`docs/specs/decisions.md`](docs/specs/decisions.md). Architecture decisions that change a spec go in [`docs/adr/`](docs/adr/).
- For anything larger than a small fix, open an issue first so we can agree on the approach.
- Report security problems privately (see the security policy), never in a public issue.

## Development

Requirements: Go ≥ 1.26, Node ≥ 22 with pnpm, git ≥ 2.40.

```bash
make install     # JS dependencies
make build       # bin/kmdn with the SPA embedded
make dev         # Go server and Vite dev server
```

## Checks

Run what CI runs before you push:

```bash
go vet ./... && golangci-lint run ./...
go test -race ./...                        # SQLite
KMDN_TEST_POSTGRES_URL=postgres://… go test -race -count=1 ./internal/...
pnpm -r typecheck && pnpm -r lint && pnpm -r test
pnpm --filter @kmdn/doc-engine bundle      # after doc-engine changes (engine.js is committed)
pnpm --filter @kmdn/api-client generate    # after api/openapi.yaml changes
make e2e                                   # Playwright against the built binary
```

## Pull requests and git history

- Git keeps the full history of validated changes: never squash, rebase away or force-push over reviewed commits. Pull requests are merged with merge commits.
- Larger work is split into stacked pull requests, each based on the previous one and merged bottom-up.
- Keep commits focused; write the message as what the change does and why.
- Update the specs, the API contract (`api/openapi.yaml`) and the docs in the same pull request as the behaviour they describe.
