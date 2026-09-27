# syntax=docker/dockerfile:1
# kmdn from source: the SPA is built with Node, then embedded in a static Go
# binary, which runs on Alpine with git (kmdn needs git ≥ 2.40).
# Releases use Dockerfile.release with GoReleaser's prebuilt binaries.

FROM node:24-alpine AS web
WORKDIR /src
RUN corepack enable
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
COPY web/package.json web/
COPY packages/doc-engine/package.json packages/doc-engine/
COPY packages/api-client/package.json packages/api-client/
RUN pnpm install --frozen-lockfile
COPY web web
COPY packages packages
COPY api api
RUN pnpm --filter @kmdn/web build

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd cmd
COPY internal internal
COPY api api
COPY --from=web /src/web/dist internal/web/dist
ARG VERSION=dev COMMIT=unknown
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X github.com/kmdn-app/kmdn/internal/version.Version=${VERSION} -X github.com/kmdn-app/kmdn/internal/version.Commit=${COMMIT}" \
      -o /out/kmdn ./cmd/kmdn

FROM alpine:3.22
RUN apk add --no-cache git ca-certificates tzdata \
 && adduser -D -H -u 10001 -s /sbin/nologin kmdn \
 && mkdir /data && chown kmdn /data
COPY --from=build /out/kmdn /usr/local/bin/kmdn
USER kmdn
ENV KMDN_DATA_DIR=/data \
    KMDN_DB_URL=sqlite:///data/kmdn.db \
    KMDN_SERVER_LISTEN=:8080
VOLUME /data
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=20s CMD wget -qO- http://127.0.0.1:8080/healthz >/dev/null || exit 1
ENTRYPOINT ["kmdn"]
CMD ["serve"]
