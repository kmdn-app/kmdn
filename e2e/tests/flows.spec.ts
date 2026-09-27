import { expect, test, type BrowserContext, type Page } from "@playwright/test";
import { ADMIN, FORGE_URL, REVIEWER } from "../env.ts";
import { emailLink, forgeFile, forgeState, logOffset, signIn, state } from "./helpers.ts";

// The core loop against a real binary and a fake GitLab: sign in, invite a
// reviewer, edit in a revision, submit, approve, publish, and find the
// attributed commit on the forge.
test.describe.serial("sign in, edit, review, publish", () => {
  let admin: BrowserContext;
  let reviewer: BrowserContext;
  let a: Page;
  let r: Page;
  const { owner, name, repoID } = state();
  const repoPath = `/${owner}/${name}`;

  test.beforeAll(async ({ browser }) => {
    admin = await browser.newContext();
    reviewer = await browser.newContext();
    a = await admin.newPage();
    r = await reviewer.newPage();
  });
  test.afterAll(async () => {
    await admin.close();
    await reviewer.close();
  });

  test("the admin signs in with an email link", async () => {
    await signIn(a, ADMIN.email);
    await a.goto(repoPath);
    await expect(a.getByRole("heading", { level: 1 })).toContainText("handbook");
    await expect(a.getByText("Initial docs")).toBeVisible();
    await expect(a.getByRole("complementary", { name: "Sidebar" }).getByText("travel.md")).toBeVisible();
  });

  test("a reviewer joins through an invite", async () => {
    const csrf = (await admin.cookies()).find((c) => c.name === "kmdn_csrf")!.value;
    const since = logOffset();
    const res = await a.request.post("/api/v1/admin/invites", { data: { email: REVIEWER.email, repo_id: repoID, role: "maintainer" }, headers: { "X-Kmdn-CSRF": csrf } });
    expect(res.status()).toBe(201);
    await r.goto(await emailLink(REVIEWER.email, since, /https?:\/\/\S+\/invite\/[\w-]+/));
    await r.getByLabel("Your name").fill(REVIEWER.name);
    await r.getByRole("button", { name: "Accept invite", exact: true }).click();
    await expect(r).not.toHaveURL(/\/invite\//);
  });

  test("the admin edits a page in a revision and submits it", async () => {
    await a.goto(`${repoPath}/docs/travel.md`);
    await expect(a.getByRole("heading", { name: "Travel", level: 1 })).toBeVisible();
    await a.getByRole("button", { name: "Edit", exact: true }).click();
    await expect(a).toHaveURL(/revision=\d+/);
    const editor = a.locator(".ProseMirror");
    await expect(editor).toHaveAttribute("contenteditable", "true");
    await editor.getByText("Book trains through the travel desk.").click();
    await a.keyboard.press("End");
    await a.keyboard.type(" Flights need a manager's approval.");
    await expect(a.getByText("Saved")).toBeVisible();
    // Materialized after the quiet period: the overview shows the change.
    await a.getByRole("link", { name: "Done" }).click();
    await a.goto(`${repoPath}/revisions/1`);
    await expect(a.getByText("docs/travel.md")).toBeVisible();
    await expect(a.getByRole("main").getByText("+1")).toBeVisible({ timeout: 15_000 });
    await a.getByRole("button", { name: "Submit for review", exact: true }).first().click();
    const dialog = a.getByRole("dialog");
    await dialog.getByText(REVIEWER.name).click();
    await dialog.getByRole("button", { name: "Send to 1 reviewer" }).click();
    await expect(a.getByText("In review").first()).toBeVisible();
  });

  test("the reviewer approves", async () => {
    await r.goto(`${repoPath}/revisions/1`);
    await expect(r.getByText("Flights need a manager's approval.").or(r.getByText("docs/travel.md")).first()).toBeVisible();
    await r.getByRole("button", { name: "Approve", exact: true }).first().click();
    await expect(r.getByText("Approved").first()).toBeVisible();
  });

  test("the admin publishes: one commit on the forge, co-signed", async () => {
    await a.goto(`${repoPath}/revisions/1`);
    await a.getByRole("button", { name: "Publish", exact: true }).first().click();
    const dialog = a.getByRole("dialog");
    await expect(dialog.getByText("Publish to main")).toBeVisible();
    await dialog.getByRole("button", { name: "Publish", exact: true }).click();
    await expect(a.getByRole("main").getByText("Published", { exact: true }).first()).toBeVisible({ timeout: 30_000 });
    await expect.poll(() => forgeFile(`${owner}/${name}`, "docs/travel.md"), { timeout: 20_000 }).toContain("Flights need a manager's approval.");
    const st = await forgeState();
    const log = st.projects.find((p) => p.path_with_namespace === `${owner}/${name}`)!.log[0]!;
    expect(log).toContain(`Co-authored-by: ${ADMIN.name}`);
    expect(log).toContain(`Reviewed-by: ${REVIEWER.name}`);
    // Readers see the published change, its history and blame (which read
    // contents through the partial mirror, with the forge's credentials).
    await r.goto(`${repoPath}/docs/travel.md`);
    await expect(r.getByText("Flights need a manager's approval.")).toBeVisible();
    await expect(r.getByText(/Last updated/)).toBeVisible();
    await r.goto(`${repoPath}/docs/travel.md?view=blame`);
    await expect(r.getByRole("main").getByText("Ana").first()).toBeVisible();
  });

  test("on a protected branch, publishing opens a merge request", async () => {
    await fetch(`${FORGE_URL}/_fake/projects`, { method: "POST", body: JSON.stringify({ path: `${owner}/${name}`, protected: true }) });
    const csrf = (await admin.cookies()).find((c) => c.name === "kmdn_csrf")!.value;
    expect((await a.request.post(`/api/v1/repos/${repoID}/refresh`, { headers: { "X-Kmdn-CSRF": csrf } })).status()).toBe(202);
    await expect.poll(async () => (await (await a.request.get(`/api/v1/repos/${repoID}`)).json()).protection?.protected, { timeout: 15_000 }).toBe(true);

    await a.goto(`${repoPath}/docs/index.md`);
    await a.getByRole("button", { name: "Edit", exact: true }).click();
    await expect(a).toHaveURL(/revision=2/);
    await a.locator(".ProseMirror").getByText("Start here.").click();
    await a.keyboard.press("End");
    await a.keyboard.type(" Ask in #docs if you're stuck.");
    await a.goto(`${repoPath}/revisions/2`);
    await expect(a.getByRole("main").getByText("+1")).toBeVisible({ timeout: 15_000 });
    await a.getByRole("button", { name: "Submit for review", exact: true }).first().click();
    await a.getByRole("dialog").getByText(REVIEWER.name).click();
    await a.getByRole("dialog").getByRole("button", { name: "Send to 1 reviewer" }).click();

    await r.goto(`${repoPath}/revisions/2`);
    await r.getByRole("button", { name: "Approve", exact: true }).first().click();
    await expect(r.getByText("Approved").first()).toBeVisible();

    await a.goto(`${repoPath}/revisions/2`);
    await a.getByRole("button", { name: "Publish", exact: true }).first().click();
    await a.getByRole("dialog").getByRole("button", { name: /Open (a )?merge request|Open (a )?pull request|Publish/ }).last().click();
    await expect.poll(async () => (await forgeState()).merge_requests.length, { timeout: 20_000 }).toBe(1);
    const mr = (await forgeState()).merge_requests[0]!;
    expect(mr.target_branch).toBe("main");
    expect(mr.source_branch).toMatch(/^kmdn\//);
    // main is untouched until the merge request is merged.
    expect(await forgeFile(`${owner}/${name}`, "docs/index.md")).not.toContain("Ask in #docs");
    expect(await forgeFile(`${owner}/${name}`, "docs/index.md", mr.source_branch)).toContain("Ask in #docs");
  });
});
