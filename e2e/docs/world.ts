// API-level setup for the documentation screenshots: people, the GitLab
// connection and the handbook repository.
import { PEOPLE, REPO } from "./content.ts";
import { FORGE_TOKEN, FORGE_URL, URL, emailLink, logSize, setupToken } from "./stack.ts";

export type Person = keyof typeof PEOPLE;

/** A cookie jar just good enough for API calls as one person. */
export class Client {
  cookies = new Map<string, string>();
  async call<T = any>(method: string, path: string, body?: unknown): Promise<T> {
    const headers: Record<string, string> = {
      "Content-Type": "application/json",
      Cookie: [...this.cookies].map(([k, v]) => `${k}=${v}`).join("; "),
    };
    const csrf = this.cookies.get("kmdn_csrf");
    if (csrf) headers["X-Kmdn-CSRF"] = csrf;
    const res = await fetch(URL + "/api/v1" + path, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    for (const c of res.headers.getSetCookie()) {
      const [kv] = c.split(";");
      const i = kv!.indexOf("=");
      if (i > 0) this.cookies.set(kv!.slice(0, i), kv!.slice(i + 1));
    }
    const text = await res.text();
    if (!res.ok) throw new Error(`${method} ${path}: ${res.status} ${text}`);
    return text ? JSON.parse(text) : (null as T);
  }
  /** Cookies for a Playwright browser context. */
  browserCookies() {
    return [...this.cookies].map(([name, value]) => ({
      name,
      value,
      url: URL,
    }));
  }
}

export async function waitUntil<T>(what: string, fn: () => Promise<T | undefined | null | false>, ms = 30_000): Promise<T> {
  const until = Date.now() + ms;
  while (Date.now() < until) {
    const v = await fn().catch(() => undefined);
    if (v) return v;
    await new Promise((r) => setTimeout(r, 250));
  }
  throw new Error(`timed out waiting for ${what}`);
}

/** Creates the admin through the setup API (the screenshot run does it in the UI). */
export async function createAdmin(): Promise<Client> {
  const maya = new Client();
  await maya.call("POST", "/setup/admin", {
    token: await setupToken(),
    name: PEOPLE.maya.name,
    email: PEOPLE.maya.email,
    instance_name: "Northwind Docs",
  });
  await maya.call("POST", "/setup/complete");
  return maya;
}

/** Signs the admin in again through an email link (after the UI setup). */
export async function signInByLink(email: string): Promise<Client> {
  const c = new Client();
  const since = logSize();
  await c.call("POST", "/auth/magic-link", { email });
  const link = await emailLink(email, since, /https?:\/\/\S+\/auth\/verify\?token=[\w-]+/);
  const token = new globalThis.URL(link).searchParams.get("token")!;
  await c.call("POST", "/auth/magic-link/verify", { token });
  return c;
}

export async function connectHandbook(maya: Client): Promise<{ repoID: string; hostID: string; org: string }> {
  const host = await maya.call("POST", "/admin/forges", {
    kind: "gitlab",
    base_url: FORGE_URL,
    display_name: "Northwind GitLab",
  });
  const [owner, name] = REPO.split("/");
  const org = (await maya.call("GET", "/orgs")).items[0].slug as string;
  const connected = await maya.call("POST", `/orgs/${org}/repos`, {
    forge_host_id: host.id,
    owner,
    name,
    token: FORGE_TOKEN,
    content_root: "docs/",
  });
  const repoID = connected.repo.id as string;
  await waitUntil("the repository to sync", async () => (await maya.call("GET", `/repos/${repoID}`)).head_sha);
  return { repoID, hostID: host.id, org };
}

const ROLES: Record<Exclude<Person, "maya">, string> = {
  sam: "maintainer",
  tom: "contributor",
  priya: "contributor",
  luis: "viewer",
};

/** Invites everyone but Maya to the repo and accepts the invites; returns a signed-in client each. */
export async function invitePeople(maya: Client, repoID: string): Promise<Record<Person, Client>> {
  const out = { maya } as Record<Person, Client>;
  for (const [who, role] of Object.entries(ROLES) as [Exclude<Person, "maya">, string][]) {
    const p = PEOPLE[who];
    const since = logSize();
    await maya.call("POST", `/repos/${repoID}/invites`, {
      email: p.email,
      role,
    });
    const link = await emailLink(p.email, since, /https?:\/\/\S+\/invite\/[\w-]+/);
    const token = link.split("/invite/")[1]!;
    const c = new Client();
    await c.call("POST", `/invites/${token}/accept`, { name: p.name });
    if (!c.cookies.has("kmdn_session")) {
      const signed = await signInByLink(p.email);
      c.cookies = signed.cookies;
    }
    out[who] = c;
  }
  return out;
}
