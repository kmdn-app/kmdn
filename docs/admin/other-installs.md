# Other installs

[Upsun](install-upsun.md) is the reference deployment. kmdn also runs anywhere you can run one binary or one container. Whatever you choose, kmdn needs:

- git 2.40 or newer on its `PATH`. The Docker image includes it.
- A writable data directory for the SQLite database, git mirrors and uploads.
- A secret key: 32 random bytes, base64. Generate one with `head -c32 /dev/urandom | base64` and keep it safe. Without it, kmdn can't decrypt the credentials it stored.
- A base URL people reach it at. Sign-in links, webhooks and OAuth callbacks use it, and passkeys are tied to its host.
- An SMTP server. People sign in with email links, and setup won't finish without working email.
- A reverse proxy that passes WebSockets through, if kmdn sits behind one. Don't buffer or cache the editor's WebSocket.

kmdn is a single node by design. Run one instance per organization.

## Docker

Generate a secret key and store it in your password manager. Every later `docker run` needs the same one:

```bash
head -c32 /dev/urandom | base64
```

```bash
docker run -d -p 8080:8080 -v kmdn-data:/data \
  -e KMDN_SECRET_KEY='<your key>' \
  -e KMDN_SERVER_BASE_URL=http://localhost:8080 \
  ghcr.io/kmdn-app/kmdn:latest
```

The image runs as a non-root user, keeps its data in `/data` and answers health checks on `/healthz`.

Find the setup link in the log:

```bash
docker logs <container> 2>&1 | grep "needs setup"
```

## Docker Compose, with PostgreSQL and TLS

`deploy/docker-compose.yml` runs kmdn with optional PostgreSQL and optional Caddy for TLS, as compose profiles:

```bash
cd deploy
cp kmdn.env.example .env
```

Set `KMDN_SECRET_KEY` and `KMDN_SERVER_BASE_URL` in `.env`, then start what you need:

```bash
docker compose up -d
```

```bash
docker compose --profile postgres --profile caddy up -d
```

With the `postgres` profile, also set `POSTGRES_PASSWORD` and `KMDN_DB_URL` in `.env`. With the `caddy` profile, set `KMDN_DOMAIN` and make the base URL `https://<domain>`. The shipped `Caddyfile` hides `/metrics` from the internet.

## Binary

Download a release for Linux or macOS, amd64 or arm64, from [the releases page](https://github.com/kmdn-app/kmdn/releases). Then:

```bash
kmdn init
```

`init` writes `kmdn.yaml` with a new secret key. Edit the base URL and SMTP settings in it, then check and start:

```bash
kmdn doctor
```

```bash
kmdn serve
```

Run it under systemd or another supervisor, and put a reverse proxy with TLS in front of it. The [configuration reference](configuration.md) lists every setting.

## From source

```bash
pnpm install
make build
```

The result is `bin/kmdn`, with the web app embedded. It needs Go 1.26.6 or newer, Node 22 or newer with pnpm, and git.

After the install, continue with [First run](first-run.md).
