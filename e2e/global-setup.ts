import { execFileSync, spawn } from "node:child_process";
import { randomBytes } from "node:crypto";
import { existsSync, mkdirSync, openSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { ADMIN, FORGE_PORT, FORGE_TOKEN, FORGE_URL, KMDN_PORT, KMDN_URL, LOG_FILE, RUN_DIR, STATE_FILE, type State } from "./env.ts";

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "..");

async function waitFor(url: string, what: string, ms = 30_000) {
  const until = Date.now() + ms;
  while (Date.now() < until) {
    try {
      if ((await fetch(url)).ok) return;
    } catch {
      /* not up yet */
    }
    await new Promise((r) => setTimeout(r, 200));
  }
  throw new Error(`${what} didn't start (${url})`);
}

/** A cookie jar just good enough for the API calls setup makes. */
class Client {
  cookies = new Map<string, string>();
  async call(method: string, path: string, body?: unknown) {
    const headers: Record<string, string> = { "Content-Type": "application/json", Cookie: [...this.cookies].map(([k, v]) => `${k}=${v}`).join("; ") };
    const csrf = this.cookies.get("kmdn_csrf");
    if (csrf) headers["X-Kmdn-CSRF"] = csrf;
    const res = await fetch(KMDN_URL + "/api/v1" + path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) });
    for (const c of res.headers.getSetCookie()) {
      const [kv] = c.split(";");
      const [k, v] = kv!.split("=");
      if (k && v !== undefined) this.cookies.set(k, v);
    }
    const text = await res.text();
    if (!res.ok) throw new Error(`${method} ${path}: ${res.status} ${text}`);
    return text ? JSON.parse(text) : null;
  }
}

export default async function setup() {
  rmSync(RUN_DIR, { recursive: true, force: true });
  mkdirSync(RUN_DIR, { recursive: true });
  const bin = process.env.KMDN_BIN ?? join(ROOT, "bin/kmdn");
  if (!existsSync(bin)) throw new Error(`${bin} is missing: run make build first (or set KMDN_BIN)`);
  const forgeBin = join(RUN_DIR, "fakeforge");
  execFileSync("go", ["build", "-o", forgeBin, "./e2e/fakeforge"], { cwd: ROOT, stdio: "inherit" });

  const pids: number[] = [];
  const forgeLog = openSync(join(RUN_DIR, "fakeforge.log"), "a");
  const forge = spawn(forgeBin, ["-listen", `127.0.0.1:${FORGE_PORT}`, "-root", join(RUN_DIR, "forge"), "-token", FORGE_TOKEN], { stdio: ["ignore", forgeLog, forgeLog], detached: true });
  pids.push(forge.pid!);
  writeFileSync(STATE_FILE, JSON.stringify({ pids })); // so teardown stops it if setup fails
  await waitFor(FORGE_URL + "/healthz", "fakeforge");
  const seed = await fetch(FORGE_URL + "/_fake/projects", {
    method: "POST",
    body: JSON.stringify({
      path: "acme/handbook",
      files: {
        "docs/index.md": "# Handbook\n\nStart here.\n\n- [Travel](travel.md)\n",
        "docs/travel.md": "# Travel\n\n## Booking\n\nBook trains through the travel desk.\n",
        "README.md": "The Acme handbook.\n",
      },
    }),
  });
  if (!seed.ok) throw new Error("seed the fake forge: " + (await seed.text()));

  const log = openSync(LOG_FILE, "a");
  const kmdn = spawn(bin, ["serve"], {
    env: {
      ...process.env,
      KMDN_DATA_DIR: join(RUN_DIR, "data"),
      KMDN_DB_URL: "sqlite://" + join(RUN_DIR, "data/kmdn.db"),
      KMDN_SECRET_KEY: randomBytes(32).toString("base64"),
      KMDN_SERVER_LISTEN: `127.0.0.1:${KMDN_PORT}`,
      KMDN_SERVER_BASE_URL: KMDN_URL,
      KMDN_SMTP_HOST: "log",
      KMDN_SMTP_FROM: "kmdn@acme.test",
      KMDN_TELEMETRY_LOG_FORMAT: "json",
    },
    stdio: ["ignore", log, log],
    detached: true,
  });
  pids.push(kmdn.pid!);
  writeFileSync(STATE_FILE, JSON.stringify({ pids }));
  await waitFor(KMDN_URL + "/healthz", "kmdn");

  // First-run setup: the admin, from the setup token in the log.
  let token = "";
  for (let i = 0; i < 50 && !token; i++) {
    token = /setup\?token=([A-Za-z0-9_-]+)/.exec(readFileSync(LOG_FILE, "utf8"))?.[1] ?? "";
    if (!token) await new Promise((r) => setTimeout(r, 100));
  }
  if (!token) throw new Error("no setup token in the kmdn log");
  const admin = new Client();
  await admin.call("POST", "/setup/admin", { token, name: ADMIN.name, email: ADMIN.email, instance_name: "Acme Docs" });
  await admin.call("POST", "/setup/complete");
  const host = await admin.call("POST", "/admin/forges", { kind: "gitlab", base_url: FORGE_URL, display_name: "Acme GitLab" });
  const connected = await admin.call("POST", "/repos", { forge_host_id: host.id, owner: "acme", name: "handbook", token: FORGE_TOKEN, content_root: "docs/" });
  const repoID = connected.repo.id as string;
  for (let i = 0; i < 100; i++) {
    const r = await admin.call("GET", `/repos/${repoID}`);
    if (r.head_sha) break;
    if (i === 99) throw new Error("the repository never synced: " + JSON.stringify(r));
    await new Promise((r) => setTimeout(r, 200));
  }
  const csrf = admin.cookies.get("kmdn_csrf") ?? "";
  const cookie = [...admin.cookies].map(([k, v]) => `${k}=${v}`).join("; ");
  const state: State = { repoID, owner: connected.repo.owner, name: connected.repo.name, pids, admin: { cookie, csrf } };
  writeFileSync(STATE_FILE, JSON.stringify(state));
}
