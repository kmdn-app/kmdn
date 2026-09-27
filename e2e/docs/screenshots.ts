// Captures the screenshots in docs/images from a real kmdn: a fresh bin/kmdn,
// the fake GitLab seeded with the sample handbook (content.ts) and a fake AI
// provider. Run from the repository root after `make build`:
//
//	pnpm --filter @kmdn/e2e screenshots            # every scene
//	pnpm --filter @kmdn/e2e screenshots page review # scenes whose name matches
//
// Scenes run in order and build on each other (the revision they edit, the
// review, the publish), so a filtered run still plays every step and only
// skips saving the other images.
import { execFileSync } from "node:child_process";
import { mkdirSync, rmSync } from "node:fs";
import { join } from "node:path";
import { chromium, type Browser, type BrowserContext, type Locator, type Page } from "@playwright/test";
import { INITIAL, PEOPLE, REPO } from "./content.ts";
import { FORGE_TOKEN, ROOT, RUN_DIR, URL, pushToMain, setupToken, startStack } from "./stack.ts";
import { Client, connectHandbook, invitePeople, signInByLink, waitUntil, type Person } from "./world.ts";

const OUT = join(ROOT, "docs/images");
const SHOTS = join(RUN_DIR, "shots");
const only = process.argv.slice(2);
const failures: string[] = [];

const stack = await startStack();
let browser: Browser | undefined;
try {
  browser = await chromium.launch();
  await run(browser);
  if (process.env.KEEP) {
    console.log(`\nKEEP is set: kmdn stays up at ${URL} until Ctrl-C`);
    const alive = setInterval(() => {}, 60_000);
    await new Promise<void>((resolve) => process.on("SIGINT", () => resolve()));
    clearInterval(alive);
  }
} finally {
  await browser?.close();
  stack.stop();
}
if (failures.length) {
  console.error(`\n${failures.length} scene(s) failed:\n  ${failures.join("\n  ")}`);
  process.exit(1);
}

async function newPage(b: Browser, client?: Client, opts: { mobile?: boolean } = {}): Promise<Page> {
  const ctx: BrowserContext = await b.newContext({
    baseURL: URL,
    viewport: opts.mobile ? { width: 390, height: 844 } : { width: 1440, height: 900 },
    deviceScaleFactor: 2,
    isMobile: opts.mobile,
    hasTouch: opts.mobile,
    colorScheme: "light",
    reducedMotion: "reduce",
  });
  if (client) await ctx.addCookies(client.browserCookies());
  return ctx.newPage();
}

/** Saves a screenshot as docs/images/<name>.webp. */
async function shot(page: Page, name: string, opts: { clip?: Locator; tall?: number } = {}) {
  if (only.length && !only.some((o) => name.includes(o))) return;
  mkdirSync(SHOTS, { recursive: true });
  mkdirSync(OUT, { recursive: true });
  const png = join(SHOTS, name + ".png");
  const size = page.viewportSize()!;
  // The app scrolls inside its panes, so a long page needs a taller window.
  if (opts.tall) await page.setViewportSize({ width: size.width, height: opts.tall });
  await page.waitForTimeout(400); // let transitions and toasts settle
  if (opts.clip) await opts.clip.screenshot({ path: png, animations: "disabled" });
  else await page.screenshot({ path: png, animations: "disabled" });
  if (opts.tall) await page.setViewportSize(size);
  execFileSync("cwebp", ["-quiet", "-q", "82", "-m", "6", png, "-o", join(OUT, name + ".webp")]);
  console.log("  ✓", name);
}

/** Opens or closes the right panel (its state is remembered per browser). */
async function panel(page: Page, open: boolean) {
  const side = page.getByRole("complementary", { name: "Side panel" });
  if ((await side.isVisible()) !== open) await page.getByRole("button", { name: "Toggle side panel" }).first().click();
  await page.waitForTimeout(300);
}

/** Runs one scene; a failure is reported and the run goes on. */
async function scene(name: string, fn: () => Promise<void>) {
  console.log(name);
  try {
    await fn();
  } catch (e) {
    // What every open page showed, to see why.
    mkdirSync(SHOTS, { recursive: true });
    let i = 0;
    for (const ctx of browser?.contexts() ?? [])
      for (const p of ctx.pages()) await p.screenshot({ path: join(SHOTS, `_failed-${name}-${i++}.png`) }).catch(() => undefined);
    failures.push(`${name}: ${(e as Error).message.split("\n")[0]}`);
    console.error("  ✗", (e as Error).message.split("\n").slice(0, 3).join(" | "));
  }
}

async function run(b: Browser) {
  const repoPath = "/" + REPO;
  rmSync(SHOTS, { recursive: true, force: true });

  // ── First run: the setup wizard, as Maya ─────────────────────────────
  let maya!: Client;
  await scene("setup", async () => {
    const p = await newPage(b);
    await p.goto(`/setup?token=${await setupToken()}`);
    await p.getByLabel("Your name").fill(PEOPLE.maya.name);
    await p.getByLabel("Your email").fill(PEOPLE.maya.email);
    await p.getByLabel("Instance name").fill("Northwind Docs");
    await shot(p, "setup-admin");
    await p.getByRole("button", { name: "Create admin account" }).click();
    await p.getByText("Email delivery (SMTP)").waitFor();
    await shot(p, "setup-email");
    await p
      .getByRole("button", { name: /Save and continue|Continue/ })
      .first()
      .click();
    await p.getByText("Connect GitHub or GitLab").waitFor();
    await p.getByRole("button", { name: "Continue" }).click();
    await p.getByText("AI provider", { exact: true }).first().waitFor();
    await shot(p, "setup-ai");
    await p.getByRole("button", { name: "Continue" }).click();
    await p.getByText("kmdn is ready").waitFor();
    await p
      .getByRole("button", { name: "Open kmdn" })
      .or(p.getByRole("link", { name: "Open kmdn" }))
      .click();
    await p.context().close();
  });
  maya = await signInByLink(PEOPLE.maya.email);

  await scene("signin", async () => {
    const p = await newPage(b);
    await p.goto("/signin");
    await p.getByLabel("Work email").fill(PEOPLE.tom.email);
    await shot(p, "signin");
    await p.getByRole("button", { name: "Email me a sign-in link" }).click();
    await p.getByText(/check your email/i).waitFor();
    await shot(p, "signin-check-email");
    await p.context().close();
  });

  // ── Admin: forge, repository, people ────────────────────────────────
  const m = await newPage(b, maya);
  await scene("admin-repositories", async () => {
    await m.goto("/admin?section=repositories");
    await m.getByRole("button", { name: "Add GitLab" }).click();
    const d = m.getByRole("dialog");
    await d.getByLabel(/URL/).first().fill("https://gitlab.northwind.test");
    await shot(m, "admin-add-gitlab");
    await m.keyboard.press("Escape");
  });
  const { repoID } = await connectHandbook(maya);
  const people = await invitePeople(maya, repoID);
  const c = (who: Person) => people[who];

  await scene("admin-repositories", async () => {
    await m.goto("/admin?section=repositories");
    await m.getByText(REPO).first().waitFor();
    await shot(m, "admin-repositories");
    await m.getByRole("button", { name: "Connect" }).last().click();
    const d = m.getByRole("dialog");
    await d.getByLabel("Project path").fill("northwind/policies");
    await d.getByLabel("Project or group access token").fill(FORGE_TOKEN.replace(/./g, "•"));
    await shot(m, "admin-connect-repo");
    await m.keyboard.press("Escape");
  });

  await scene("admin-users", async () => {
    await m.goto("/admin?section=users");
    await m.getByText(PEOPLE.luis.email).waitFor();
    await shot(m, "admin-users");
    await m.getByRole("button", { name: "Invite user" }).click();
    await m.getByRole("dialog").getByLabel("Email").fill("ines@northwind.test");
    await shot(m, "admin-invite");
    await m.keyboard.press("Escape");
  });

  await scene("admin-groups", async () => {
    const g = await maya.call("POST", "/orgs/default/admin/groups", {
      name: "People team",
      description: "HR and people operations",
    });
    for (const who of ["tom", "maya"] as Person[]) {
      const me = await c(who).call("GET", "/me");
      await maya.call("PUT", `/orgs/default/admin/groups/${g.id}/members/${me.user?.id ?? me.id}`);
    }
    await maya.call("PUT", `/repos/${repoID}/members/group/${g.id}`, {
      role: "contributor",
    });
    await maya.call("POST", "/orgs/default/admin/groups", {
      name: "Platform",
      description: "Engineering on-call and deploys",
    });
    await m.goto("/admin?section=groups");
    await m.getByText("People team").first().waitFor();
    await shot(m, "admin-groups");
  });

  // ── Reading ─────────────────────────────────────────────────────────
  const luis = await newPage(b, c("luis"));
  await scene("home", async () => {
    const t = await newPage(b, c("tom"));
    await t.goto(repoPath);
    await t.getByText("What do you want to change?").waitFor();
    await shot(t, "home");
    await t.context().close();
  });

  await scene("page", async () => {
    await luis.goto(`${repoPath}/docs/onboarding/first-week.md`);
    await luis.getByRole("heading", { name: "Your first week", level: 1 }).waitFor();
    await shot(luis, "page");
    await luis.goto(`${repoPath}/docs/onboarding/it-setup.md`); // read before the change, for the banner later
    await luis.getByRole("heading", { name: "IT setup", level: 1 }).waitFor();
  });

  await scene("search", async () => {
    await luis.goto(`${repoPath}/docs/index.md`);
    await luis.getByRole("heading", { name: "Northwind handbook", level: 1 }).waitFor();
    await luis.keyboard.press("Meta+k");
    await luis.keyboard.type("abroad");
    await luis.waitForTimeout(800);
    await shot(luis, "search");
    await luis.keyboard.press("Escape");
  });

  await scene("history", async () => {
    await luis.goto(`${repoPath}/docs/engineering/deploy-runbook.md?view=blame`);
    await luis.getByRole("main").getByText("Maya Chen").first().waitFor();
    await shot(luis, "blame");
  });

  // ── A revision: Tom edits, Priya joins ──────────────────────────────
  const tom = await newPage(b, c("tom"));
  const editor = (p: Page) => p.locator(".ProseMirror").first();
  await scene("editor", async () => {
    await tom.goto(`${repoPath}/docs/onboarding/it-setup.md`);
    await tom.getByRole("button", { name: "Edit", exact: true }).click();
    await tom.waitForURL(/revision=\d+/);
    await tom.getByText("Synced").waitFor();
    await tom.waitForTimeout(1500);
    // The editor can remount right after the revision starts and drop the
    // caret: check where the text landed, erase it and retry if it's wrong.
    const item = editor(tom).locator("li", { hasText: "Add your laptop to Find My." });
    for (let i = 0; i < 4 && !(await item.count()); i++) {
      if (i) {
        for (let k = 0; k <= "Add your laptop to Find My.".length; k++) await tom.keyboard.press("Backspace");
        await tom.waitForTimeout(1500);
      }
      await editor(tom).getByText("Install the apps from Self Service.").click();
      await tom.keyboard.press("End");
      await tom.keyboard.press("Enter");
      await tom.keyboard.type("Add your laptop to Find My.");
      await tom.waitForTimeout(500);
    }
    await item.waitFor();
    await tom.getByText("Synced").waitFor();
    await shot(tom, "editor");
  });
  const revNumber = Number(new globalThis.URL(tom.url()).searchParams.get("revision") ?? 1);
  const rev = await c("tom")
    .call("GET", `/repos/${repoID}/revisions/by-number/${revNumber}`)
    .catch(() => undefined);

  await scene("save-all", async () => {
    await tom.getByRole("button", { name: /Save all/ }).click();
    await tom.getByText(/Saved as commit [0-9a-f]{7}/).waitFor({ timeout: 20_000 });
    await shot(tom, "save-all");
  });

  const priya = await newPage(b, c("priya"));
  await scene("presence", async () => {
    // Tom adds Priya as an editor.
    if (rev?.id) {
      const me = await c("priya").call("GET", "/me");
      await c("tom").call("PUT", `/revisions/${rev.id}/members/${me.user?.id ?? me.id}`, { role: "editor" });
    }
    await priya.goto(`${repoPath}/docs/onboarding/it-setup.md?revision=${revNumber}`);
    await priya.getByText("Synced").waitFor();
    await priya.waitForTimeout(1500);
    await editor(priya).getByText("Every new hire gets a MacBook Pro.").click();
    await priya.keyboard.press("Home");
    await tom.bringToFront();
    await tom.waitForTimeout(1500);
    await shot(tom, "presence");
  });

  await scene("source", async () => {
    await priya.getByRole("radio", { name: "Markdown" }).click();
    await priya.locator(".cm-content").first().waitFor();
    await shot(priya, "source-mode");
    await priya.getByRole("radio", { name: "Visual" }).click();
  });

  await scene("suggesting", async () => {
    await priya
      .getByRole("button", {
        name: /Suggesting|Record your edits as suggestions/,
      })
      .first()
      .click();
    await editor(priya).getByText("Ask your manager for anything else.").click();
    await priya.keyboard.press("End");
    await priya.keyboard.type(" Request extra software in Self Service.");
    await priya.waitForTimeout(1000);
    await tom.bringToFront();
    await tom.waitForTimeout(1000);
    await shot(tom, "suggesting");
  });

  await scene("assistant-revision", async () => {
    await tom
      .getByRole("button", { name: "Toggle side panel" })
      .click()
      .catch(() => undefined);
    await tom.getByRole("tab", { name: "Assistant" }).click();
    await tom.getByLabel("Ask the assistant…").fill("Make the laptop refresh cycle match the laptop policy");
    await tom.keyboard.press("Enter");
    await tom.getByText(/I suggested one change/).waitFor({ timeout: 30_000 });
    await shot(tom, "assistant-revision");
  });

  await scene("comments", async () => {
    await tom.getByRole("tab", { name: "Comments" }).click();
    await tom.waitForTimeout(800);
    await shot(tom, "suggestions-panel");
    // Tom accepts every pending suggestion (Priya's and the assistant's).
    await tom.getByRole("button", { name: "Review all" }).first().click();
    await tom
      .getByRole("menuitem", { name: "Accept all" })
      .or(tom.getByRole("button", { name: "Accept all" }))
      .first()
      .click();
    await tom.waitForTimeout(1000);
  });

  await scene("save-again", async () => {
    await tom.getByRole("button", { name: /Save all/ }).click();
    await tom.getByText(/Saved as commit [0-9a-f]{7}/).waitFor({ timeout: 20_000 });
  });

  await scene("revision-overview", async () => {
    await tom.goto(`${repoPath}/revisions/${revNumber}`);
    await tom.getByText("docs/onboarding/it-setup.md").first().waitFor();
    await panel(tom, false);
    await tom.getByText("Checking…").first().waitFor({ state: "hidden", timeout: 30_000 }).catch(() => undefined);
    await tom.waitForTimeout(800);
    await shot(tom, "revision-overview", { tall: 1700 });
  });

  await scene("submit", async () => {
    await tom.getByRole("button", { name: "Submit for review", exact: true }).first().click();
    const d = tom.getByRole("dialog");
    await d.getByText(PEOPLE.sam.name).click();
    await d.getByText(PEOPLE.maya.name).click();
    await shot(tom, "submit-for-review");
    await d.getByRole("button", { name: /Send to 2 reviewers/ }).click();
    await tom.getByText("In review").first().waitFor();
  });

  // ── Review: Sam ─────────────────────────────────────────────────────
  const sam = await newPage(b, c("sam"));
  await scene("review", async () => {
    await sam.goto(`${repoPath}/docs/onboarding/it-setup.md?revision=${revNumber}`);
    await sam.getByText("Synced").waitFor();
    await sam.waitForTimeout(1500);
    await shot(sam, "review-result");
    await sam.getByRole("radio", { name: "Changes" }).click();
    await sam.waitForTimeout(800);
    await shot(sam, "review-changes");
    await sam.getByRole("radio", { name: "Source diff" }).click();
    await sam.waitForTimeout(800);
    await shot(sam, "review-source-diff");
    await sam.getByRole("radio", { name: "Result" }).click();
  });

  await scene("review-comment", async () => {
    await editor(sam).getByText("Add your laptop to Find My.").click({ clickCount: 3 });
    await sam
      .getByRole("button", { name: /Comment/ })
      .first()
      .click();
    await sam
      .getByPlaceholder(/Add a comment/)
      .first()
      .fill("Can we link to the Find My setup guide here?");
    await sam.getByRole("button", { name: "Comment", exact: true }).last().click();
    await sam.waitForTimeout(800);
    await shot(sam, "review-comment");
  });

  await scene("approve", async () => {
    await sam.goto(`${repoPath}/revisions/${revNumber}`);
    await sam.getByRole("button", { name: "Approve", exact: true }).first().click();
    await sam
      .getByText(/Approved 1 of 2/)
      .first()
      .waitFor();
    await shot(sam, "approved-1-of-2");
  });

  // ── Updates from Published and a conflict ───────────────────────────
  await scene("updates", async () => {
    pushToMain(PEOPLE.priya, "IT setup: laptops for engineers", {
      "docs/onboarding/it-setup.md": INITIAL["docs/onboarding/it-setup.md"]!.replace(
        "Every new hire gets a MacBook Pro. Laptops are refreshed every four years.",
        "Every new hire gets a MacBook Pro; engineers can ask for a Linux laptop. Laptops are refreshed every five years.",
      ),
    });
    await maya.call("POST", `/repos/${repoID}/refresh`);
    await sam.goto(`${repoPath}/docs/onboarding/it-setup.md?revision=${revNumber}`);
    await sam
      .getByText(/Published changed 1 page/)
      .first()
      .waitFor({ timeout: 30_000 });
    await sam.waitForTimeout(800);
    await shot(sam, "updates-banner");
    await sam.getByRole("button", { name: "Review and apply" }).first().click();
    await sam.waitForTimeout(1000);
    await shot(sam, "updates-preview");
    await sam.getByRole("button", { name: "Apply updates" }).first().click();
    await sam
      .getByText(/conflict/i)
      .first()
      .waitFor();
  });

  await scene("conflict", async () => {
    await tom.goto(`${repoPath}/docs/onboarding/it-setup.md?revision=${revNumber}`);
    await tom.getByText("Conflict with Published").first().waitFor({ timeout: 20_000 });
    await tom.waitForTimeout(1000);
    await shot(tom, "conflict");
    await tom.getByRole("button", { name: "Keep this revision" }).first().click();
    await tom.waitForTimeout(1500);
    // Bring in what Published added, by hand.
    await editor(tom).getByText("says.", { exact: false }).first().click();
    await tom.keyboard.press("End");
    await tom.keyboard.type(" Engineers can ask for a Linux laptop.");
    await tom.getByText("Synced").waitFor();
    await tom.waitForTimeout(1000);
    await tom.getByRole("button", { name: /Save all/ }).click();
    await tom.getByText(/Saved as commit [0-9a-f]{7}/).waitFor({ timeout: 20_000 });
    await tom.goto(`${repoPath}/revisions/${revNumber}`);
    await tom
      .getByRole("button", { name: /Submit for review|Resubmit/ })
      .first()
      .click();
    await tom
      .getByRole("dialog")
      .getByRole("button", { name: /Send to 2 reviewers/ })
      .click();
    await tom.getByText("In review").first().waitFor();
  });

  // ── Approvals and publish ───────────────────────────────────────────
  await scene("publish", async () => {
    const r = await c("sam").call("GET", `/repos/${repoID}/revisions/by-number/${revNumber}`);
    await c("sam").call("POST", `/revisions/${r.id}/approve`);
    await c("maya").call("POST", `/revisions/${r.id}/approve`);
    await m.goto(`${repoPath}/revisions/${revNumber}`);
    await panel(m, false);
    await m.getByText("Checking…").first().waitFor({ state: "hidden", timeout: 30_000 }).catch(() => undefined);
    await m.getByRole("button", { name: "Publish", exact: true }).first().click();
    const d = m.getByRole("dialog");
    await d.getByText(/Publish to main/).waitFor();
    await m.waitForTimeout(800);
    await shot(m, "publish-dialog");
    await d.getByRole("button", { name: "Publish", exact: true }).click();
    await m.getByRole("main").getByText("Published", { exact: true }).first().waitFor({ timeout: 30_000 });
    await m.waitForTimeout(1500);
    await shot(m, "revision-published");
  });

  // ── Readers ─────────────────────────────────────────────────────────
  await scene("page-updated", async () => {
    await luis.goto(`${repoPath}/docs/onboarding/it-setup.md`);
    await luis
      .getByText(/since your last visit/)
      .first()
      .waitFor({ timeout: 30_000 });
    await luis.waitForTimeout(800);
    await shot(luis, "page-updated");
  });

  await scene("discussion", async () => {
    await luis.goto(`${repoPath}/docs/policies/remote-work.md`);
    await luis.getByText("You can work from another country").click({ clickCount: 3 });
    await luis
      .getByRole("button", { name: /Comment/ })
      .first()
      .click();
    await luis
      .getByPlaceholder(/Add a comment/)
      .first()
      .fill("The travel page says 20 days. Which one is right?");
    await luis.getByRole("button", { name: "Comment", exact: true }).last().click();
    await luis.waitForTimeout(1000);
    await shot(luis, "discussion");
  });

  await scene("assistant-qa", async () => {
    await luis.goto(repoPath);
    await luis.getByPlaceholder(/Describe a change, or ask anything/).fill("How many days can I work abroad?");
    await luis.keyboard.press("Enter");
    await luis.getByText(/two pages disagree/).waitFor({ timeout: 30_000 });
    await luis.waitForTimeout(800);
    await shot(luis, "assistant-qa");
  });

  await scene("history-panel", async () => {
    await luis.goto(`${repoPath}/docs/onboarding/it-setup.md`);
    if (!(await luis.getByRole("tab", { name: "History" }).isVisible())) await luis.getByRole("button", { name: "Toggle side panel" }).first().click();
    await luis.getByRole("tab", { name: "History" }).click();
    await luis.waitForTimeout(1000);
    await shot(luis, "history");
    await luis.getByRole("tab", { name: "Links" }).click();
    await luis.waitForTimeout(1000);
    await shot(luis, "links");
  });

  await scene("mobile", async () => {
    const phone = await newPage(b, c("luis"), { mobile: true });
    await phone.goto(`${repoPath}/docs/onboarding/first-week.md`);
    await phone.getByRole("heading", { name: "Your first week", level: 1 }).waitFor();
    await shot(phone, "mobile");
    await phone.context().close();
  });

  await scene("consistency", async () => {
    await m.goto(`${repoPath}/consistency`);
    await m.getByRole("button", { name: "Run now" }).click();
    await m
      .getByText(/working days/)
      .first()
      .waitFor({ timeout: 60_000 });
    await m.waitForTimeout(1000);
    await shot(m, "consistency");
  });

  await scene("graph", async () => {
    await m.goto(`${repoPath}/graph`);
    await m.waitForTimeout(2500);
    await m.mouse.move(820, 470);
    for (let i = 0; i < 4; i++) await m.mouse.wheel(0, -250);
    await m.waitForTimeout(1500);
    await shot(m, "graph");
  });

  await scene("revisions-list", async () => {
    // A second revision so the list has more than one state.
    await c("priya").call("POST", `/repos/${repoID}/revisions`, {
      title: "Incident response: paging rules",
      description: "Who gets paged, and when.",
    });
    await m.goto(`${repoPath}/revisions?filter=all`);
    await m.waitForTimeout(1200);
    await shot(m, "revisions-list");
  });

  await scene("inbox", async () => {
    await tom.goto("/inbox");
    await tom.waitForTimeout(1200);
    await shot(tom, "inbox");
  });

  await scene("profile", async () => {
    await tom.goto("/settings/profile");
    await tom.getByText("Co-author email").first().waitFor();
    await shot(tom, "profile");
  });

  // ── Repository settings ─────────────────────────────────────────────
  await scene("repo-settings", async () => {
    await m.goto(`${repoPath}/settings`);
    await m.getByText("Target branch").first().waitFor();
    await shot(m, "repo-settings-general");
    await m.goto(`${repoPath}/settings?section=members`);
    await m.getByText(PEOPLE.luis.name).first().waitFor();
    await shot(m, "repo-settings-members");
    await m.goto(`${repoPath}/settings?section=hooks`);
    await m.getByRole("button", { name: "Add webhook" }).first().click();
    await m.waitForTimeout(600);
    await shot(m, "repo-settings-webhook");
    await m.keyboard.press("Escape");
  });

  // ── Admin console ───────────────────────────────────────────────────
  await scene("admin-email", async () => {
    await m.goto("/admin?section=email");
    await m.waitForTimeout(1000);
    await shot(m, "admin-email");
  });
  await scene("admin-ai", async () => {
    await m.goto("/admin?section=ai");
    await m.waitForTimeout(1500);
    await shot(m, "admin-ai", { tall: 1700 });
  });
  await scene("admin-agent-keys", async () => {
    await m.goto("/admin?section=agent-keys");
    await m.getByRole("button", { name: "Create agent key" }).first().click();
    const d = m.getByRole("dialog");
    await d.getByLabel("Name").fill("Support bot");
    await d
      .getByText("All repositories")
      .first()
      .click()
      .catch(() => undefined);
    await shot(m, "admin-agent-key-create");
    await d
      .getByRole("button", { name: /Create/ })
      .last()
      .click();
    await m
      .getByText(/is ready/)
      .first()
      .waitFor();
    // Hide the secret: the image shows where it appears, not a usable key.
    await m.evaluate(() => {
      const walk = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
      for (let n = walk.nextNode(); n; n = walk.nextNode()) n.textContent = n.textContent!.replace(/kmdn_ak_[\w-]+/g, "kmdn_ak_••••••••••••••••••••••••");
      for (const el of document.querySelectorAll("input, textarea")) {
        const i = el as HTMLInputElement;
        i.value = i.value.replace(/kmdn_ak_[\w-]+/g, "kmdn_ak_••••••••••••••••••••••••");
      }
    });
    await shot(m, "admin-agent-key-created");
    await m.getByRole("button", { name: "Done" }).click();
    await m.waitForTimeout(600);
    await shot(m, "admin-agent-keys");
  });
  await scene("admin-audit", async () => {
    await m.goto("/admin?section=audit");
    await m.waitForTimeout(1200);
    await shot(m, "admin-audit");
  });
  await scene("admin-system", async () => {
    await m.goto("/admin?section=system");
    await m.getByRole("button", { name: "Run checks" }).click();
    await m.waitForTimeout(4000);
    await shot(m, "admin-system", { tall: 1300 });
  });
}
