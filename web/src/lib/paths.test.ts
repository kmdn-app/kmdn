import { isExternal, resolveRelative, slugify } from "./paths";

test("resolveRelative", () => {
  expect(resolveRelative("docs/onboarding/first-week.md", "it-setup.md")).toBe("docs/onboarding/it-setup.md");
  expect(resolveRelative("docs/onboarding/first-week.md", "../policies/remote.md")).toBe("docs/policies/remote.md");
  expect(resolveRelative("docs/onboarding/first-week.md", "./images/desk.png")).toBe("docs/onboarding/images/desk.png");
  expect(resolveRelative("docs/a.md", "/README.md")).toBe("README.md");
});

test("isExternal and slugify", () => {
  expect(isExternal("https://x.dev")).toBe(true);
  expect(isExternal("mailto:a@b")).toBe(true);
  expect(isExternal("../a.md")).toBe(false);
  expect(slugify("Your laptop (2026)!")).toBe("your-laptop-2026");
  expect(slugify("Día 1: get set up")).toBe("día-1-get-set-up");
});
