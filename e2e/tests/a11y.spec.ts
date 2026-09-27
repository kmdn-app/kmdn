import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";
import { ADMIN, REVIEWER } from "../env.ts";
import { signIn, state } from "./helpers.ts";

// WCAG 2.2 AA checks with axe on the main screens (docs/specs/02-ux.md#accessibility).
// Runs after the flows, so revisions and history exist.
const { owner, name } = state();
const repo = `/${owner}/${name}`;

async function audit(page: Page, label: string) {
  // Let data and fonts settle before scanning.
  await page.waitForLoadState("networkidle");
  const res = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"]).analyze();
  const found = res.violations.map(
    (v) => `${label}: ${v.id} (${v.impact}) ${v.help}\n    ${v.nodes.slice(0, 4).map((n) => `${n.target.join(" ")} — ${(n.any[0]?.message ?? n.failureSummary ?? "").split("\n")[0]}`).join("\n    ")}`,
  );
  expect.soft(found, found.join("\n")).toEqual([]);
}

test("sign-in page", async ({ page }) => {
  await page.goto("/signin");
  await audit(page, "signin");
});

// One sign-in for all screens (magic links are rate limited per email),
// with soft assertions so a run reports every screen's violations.
test("signed-in screens", async ({ page }) => {
  test.setTimeout(120_000);
  await signIn(page, ADMIN.email);
  for (const [label, path] of [
    ["repo home", repo],
    ["published page", `${repo}/docs/travel.md`],
    ["blame", `${repo}/docs/travel.md?view=blame`],
    ["revisions", `${repo}/revisions`],
    ["revision overview", `${repo}/revisions/1`],
    ["revision page", `${repo}/docs/index.md?revision=2`],
    ["graph", `${repo}/graph`],
    ["inbox", "/inbox"],
    ["profile", "/settings/profile"],
    ["admin users", "/admin?section=users"],
    ["admin audit", "/admin?section=audit"],
    ["admin system", "/admin?section=system"],
  ] as const) {
    await page.goto(path);
    await audit(page, label);
  }
  await page.goto(repo);
  await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
  await page.keyboard.press("ControlOrMeta+k");
  await expect(page.getByRole("dialog")).toBeVisible();
  await audit(page, "palette");
});

test("keyboard: skip link, palette; live region: collaborators", async ({ page, browser }) => {
  await signIn(page, ADMIN.email);
  await page.goto(repo);
  await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
  // The first Tab reaches the skip link, which moves focus to the content.
  await page.keyboard.press("Tab");
  await expect(page.getByRole("link", { name: "Skip to content" })).toBeFocused();
  await page.keyboard.press("Enter");
  await expect(page.locator("#main")).toBeFocused();
  // The command palette opens, searches and navigates without a mouse.
  await page.keyboard.press("ControlOrMeta+k");
  await page.keyboard.type("travel");
  await expect(page.getByRole("option", { name: /travel/i }).first()).toBeVisible();
  await page.keyboard.press("Enter");
  await expect(page).toHaveURL(/docs\/travel\.md/);
  await page.keyboard.press("Escape");

  // Someone opening the same revision is announced to screen readers.
  await page.goto(`${repo}/docs/index.md?revision=2`);
  await expect(page.locator(".ProseMirror")).toBeVisible();
  const other = await browser.newContext();
  const r = await other.newPage();
  await signIn(r, REVIEWER.email);
  await r.goto(`${repo}/docs/index.md?revision=2`);
  await expect(page.locator('[data-testid="announcer"]')).toHaveText(`${REVIEWER.name} joined the revision`, { timeout: 15_000 });
  await other.close();
});
