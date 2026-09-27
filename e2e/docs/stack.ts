// Starts what the documentation screenshots need: the fake GitLab seeded
// with the sample handbook, the fake AI provider, and a fresh bin/kmdn.
import { execFileSync, spawn } from "node:child_process";
import { randomBytes } from "node:crypto";
import { existsSync, mkdirSync, mkdtempSync, openSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { INITIAL, LATER, PEOPLE, REPO } from "./content.ts";
import { startFakeAI } from "./fake-ai.ts";

export const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "../..");
export const PORT = Number(process.env.KMDN_DOCS_PORT ?? 18480);
export const FORGE_PORT = PORT + 10;
export const AI_PORT = PORT + 19;
// localhost, not 127.0.0.1: the screenshots show this URL.
export const URL = `http://localhost:${PORT}`;
export const FORGE_URL = `http://127.0.0.1:${FORGE_PORT}`;
export const FORGE_TOKEN = "glpat-docs-token";
export const RUN_DIR = process.env.KMDN_DOCS_DIR ?? join(tmpdir(), "kmdn-docs");
export const LOG_FILE = join(RUN_DIR, "kmdn.log");

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

function git(cwd: string, ...args: string[]) {
  return execFileSync("git", args, {
    cwd,
    encoding: "utf8",
    env: { ...process.env, GIT_CONFIG_GLOBAL: "/dev/null" },
  });
}

/** Commits files straight to main on the fake forge, as someone else would. */
export function pushToMain(author: { name: string; email: string }, message: string, files: Record<string, string>) {
  const bare = join(RUN_DIR, "forge", REPO + ".git");
  const work = mkdtempSync(join(tmpdir(), "kmdn-docs-push-"));
  try {
    git(work, "clone", "-q", bare, ".");
    for (const [p, body] of Object.entries(files)) {
      mkdirSync(dirname(join(work, p)), { recursive: true });
      writeFileSync(join(work, p), body);
    }
    git(work, "add", "-A");
    git(work, "-c", `user.name=${author.name}`, "-c", `user.email=${author.email}`, "commit", "-q", "-m", message);
    git(work, "push", "-q", "origin", "HEAD:main");
  } finally {
    rmSync(work, { recursive: true, force: true });
  }
}

export async function startStack() {
  for (const u of [URL + "/healthz", FORGE_URL + "/healthz"])
    if (
      await fetch(u).then(
        (r) => r.ok,
        () => false,
      )
    )
      throw new Error(`${u} already answers: stop the other run first`);
  rmSync(RUN_DIR, { recursive: true, force: true });
  mkdirSync(RUN_DIR, { recursive: true });
  const bin = process.env.KMDN_BIN ?? join(ROOT, "bin/kmdn");
  if (!existsSync(bin)) throw new Error(`${bin} is missing: run make build first (or set KMDN_BIN)`);
  const forgeBin = join(RUN_DIR, "fakeforge");
  execFileSync("go", ["build", "-o", forgeBin, "./e2e/fakeforge"], {
    cwd: ROOT,
    stdio: "inherit",
  });

  const children: { kill: () => void }[] = [];
  const stop = () => children.forEach((c) => c.kill());
  try {
    const forgeLog = openSync(join(RUN_DIR, "fakeforge.log"), "a");
    children.push(
      spawn(forgeBin, ["-listen", `127.0.0.1:${FORGE_PORT}`, "-root", join(RUN_DIR, "forge"), "-token", FORGE_TOKEN], { stdio: ["ignore", forgeLog, forgeLog] }),
    );
    await waitFor(FORGE_URL + "/healthz", "fakeforge");
    const seed = await fetch(FORGE_URL + "/_fake/projects", {
      method: "POST",
      body: JSON.stringify({
        path: REPO,
        files: { "README.md": INITIAL["README.md"] },
      }),
    });
    if (!seed.ok) throw new Error("seed the fake forge: " + (await seed.text()));
    pushToMain(PEOPLE.maya, "Import the handbook", INITIAL);
    for (const c of LATER) pushToMain(c.author, c.message, c.files);

    const ai = await startFakeAI(AI_PORT);
    children.push({ kill: () => ai.close() });

    const log = openSync(LOG_FILE, "a");
    children.push(
      spawn(bin, ["serve"], {
        env: {
          ...process.env,
          KMDN_DATA_DIR: join(RUN_DIR, "data"),
          KMDN_SECRET_KEY: randomBytes(32).toString("base64"),
          KMDN_SERVER_LISTEN: `127.0.0.1:${PORT}`,
          KMDN_SERVER_BASE_URL: URL,
          KMDN_SMTP_HOST: "log",
          KMDN_SMTP_FROM: "Northwind Docs <docs@northwind.test>",
          KMDN_TELEMETRY_LOG_FORMAT: "json",
          // Like an Upsun deployment: the provider comes from variables.
          KMDN_ASSISTANT_PROVIDER: "anthropic",
          KMDN_ASSISTANT_API_KEY: "sk-ant-docs",
          KMDN_ASSISTANT_BASE_URL: `http://127.0.0.1:${AI_PORT}`,
          KMDN_ASSISTANT_EMBEDDINGS_BASE_URL: `http://127.0.0.1:${AI_PORT}/v1`,
          KMDN_ASSISTANT_EMBEDDINGS_API_KEY: "sk-docs",
          KMDN_ASSISTANT_EMBEDDINGS_MODEL: "text-embedding-3-small",
          KMDN_HOOKS_ALLOW_PRIVATE: "true",
        },
        stdio: ["ignore", log, log],
      }),
    );
    await waitFor(URL + "/healthz", "kmdn");
  } catch (e) {
    stop();
    throw e;
  }
  return { stop };
}

/** The one-time setup token kmdn prints while no admin exists. */
export async function setupToken(): Promise<string> {
  for (let i = 0; i < 100; i++) {
    const token = /setup\?token=([A-Za-z0-9_-]+)/.exec(readFileSync(LOG_FILE, "utf8"))?.[1];
    if (token) return token;
    await new Promise((r) => setTimeout(r, 100));
  }
  throw new Error("no setup token in the kmdn log");
}

/** The first link in the newest email to `to` written after byte `since` of the log. */
export async function emailLink(to: string, since: number, pattern: RegExp): Promise<string> {
  for (let i = 0; i < 150; i++) {
    const mails = readFileSync(LOG_FILE, "utf8")
      .slice(since)
      .split("\n")
      .filter((l) => l.includes("smtp.host=log"))
      .map((l) => JSON.parse(l) as { to: string; text: string })
      .filter((m) => m.to === to);
    const link = mails.at(-1)?.text.match(pattern)?.[0];
    if (link) return link;
    await new Promise((r) => setTimeout(r, 100));
  }
  throw new Error(`no email to ${to}`);
}

export function logSize(): number {
  return readFileSync(LOG_FILE).length;
}
