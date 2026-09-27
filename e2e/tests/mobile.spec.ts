import { expect, test } from "@playwright/test";
import { ADMIN } from "../env";
import { signIn, state } from "./helpers";

// Phones read, comment and review; they don't edit (docs/specs/02-ux.md#mobile).
const { owner, name } = state();
const repo = `/${owner}/${name}`;

test("phone: read, navigate with the sidebar sheet, review without editing", async ({ page }) => {
  await signIn(page, ADMIN.email);
  await page.goto(repo);
  await page.getByRole("button", { name: "Toggle sidebar" }).click();
  const sidebar = page.getByRole("complementary", { name: "Sidebar" });
  await expect(sidebar).toBeVisible();
  await expect(sidebar.getByRole("button", { name: "New revision" })).toBeHidden();
  await sidebar.getByRole("link", { name: "travel.md" }).click();
  await expect(page.getByRole("heading", { name: "Travel", level: 1 })).toBeVisible();
  await expect(page.getByRole("button", { name: "Edit", exact: true })).toHaveCount(0);

  await page.goto(`${repo}/docs/index.md?revision=2`);
  await expect(page.getByText("Editing works on tablets and computers.")).toBeVisible();
  await expect(page.locator(".ProseMirror")).toHaveAttribute("contenteditable", "false");
  await expect(page.getByRole("radiogroup", { name: /view/i })).toBeVisible();
});
