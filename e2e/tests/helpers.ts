import { readFileSync, statSync } from "node:fs";
import { expect, type Page } from "@playwright/test";
import { FORGE_URL, LOG_FILE, STATE_FILE, type State } from "../env";

export function state(): State {
  return JSON.parse(readFileSync(STATE_FILE, "utf8")) as State;
}

/** Where the kmdn log ends now: emails after this offset are new. */
export function logOffset(): number {
  return statSync(LOG_FILE).size;
}

/**
 * The first link in the newest email to `to` written after `since`
 * (smtp.host is "log": kmdn writes emails to its log).
 */
export async function emailLink(to: string, since: number, pattern = /https?:\/\/[^\s)]+/): Promise<string> {
  for (let i = 0; i < 100; i++) {
    const tail = readFileSync(LOG_FILE, "utf8").slice(since);
    const mails = tail
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

/** Signs in with an email link. */
export async function signIn(page: Page, email: string) {
  await page.goto("/signin");
  const since = logOffset();
  await page.getByLabel("Work email").fill(email);
  await page.getByRole("button", { name: "Email me a sign-in link" }).click();
  await expect(page.getByText(/check your email/i)).toBeVisible();
  await page.goto(await emailLink(email, since, /https?:\/\/\S+\/auth\/verify\?token=[\w-]+/));
  await expect(page).not.toHaveURL(/\/(signin|auth\/verify)/);
}

export async function forgeFile(project: string, path: string, ref = "main"): Promise<string> {
  const res = await fetch(`${FORGE_URL}/_fake/file?project=${encodeURIComponent(project)}&path=${encodeURIComponent(path)}&ref=${encodeURIComponent(ref)}`);
  return res.ok ? res.text() : "";
}

export type ForgeState = {
  projects: { path_with_namespace: string; branches: Record<string, string>; log: string[] }[];
  merge_requests: { source_branch: string; target_branch: string; title: string; web_url: string }[];
};

export async function forgeState(): Promise<ForgeState> {
  return (await fetch(`${FORGE_URL}/_fake/state`)).json() as Promise<ForgeState>;
}
