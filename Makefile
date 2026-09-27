# kmdn build tasks. `make help` lists targets.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X github.com/kmdn-app/kmdn/internal/version.Version=$(VERSION) \
	-X github.com/kmdn-app/kmdn/internal/version.Commit=$(COMMIT) \
	-X github.com/kmdn-app/kmdn/internal/version.Date=$(DATE)

.PHONY: help install web build docker e2e test test-go test-ts lint lint-go lint-ts generate dev dev-server dev-web clean

help: ## Show targets
	@grep -E '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*## "}{printf "  %-12s %s\n",$$1,$$2}'

install: ## Install JS dependencies
	pnpm install --frozen-lockfile

web: ## Build the SPA and copy it into the Go embed dir
	pnpm --filter @kmdn/web build
	find internal/web/dist -mindepth 1 ! -name .gitkeep -exec rm -rf {} +
	cp -R web/dist/. internal/web/dist/

build: web ## Build bin/kmdn with the SPA embedded
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/kmdn ./cmd/kmdn

docker: ## Build the kmdn:dev image from source
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) -t kmdn:dev .

e2e: build ## End-to-end tests (Playwright) against bin/kmdn and a fake GitLab
	pnpm --filter @kmdn/e2e e2e

test: test-go test-ts ## Run all tests

test-go:
	go test -race ./...

test-ts:
	pnpm -r test

lint: lint-go lint-ts ## Run linters

lint-go:
	go vet ./...
	golangci-lint run ./...

lint-ts:
	pnpm -r typecheck
	pnpm -r lint

generate: ## Run code generators
	go generate ./...

dev: ## Run Go server and Vite dev server (open http://localhost:5173)
	$(MAKE) -j2 dev-server dev-web

dev-server:
	go run ./cmd/kmdn serve

dev-web:
	pnpm --filter @kmdn/web dev

clean:
	rm -rf bin web/dist
	find internal/web/dist -mindepth 1 ! -name .gitkeep -exec rm -rf {} +
