import { defineConfig, devices } from "@playwright/test";
import { KMDN_URL } from "./env.ts";

// End-to-end tests against the built binary (bin/kmdn, or KMDN_BIN) and a
// fake GitLab (e2e/fakeforge). See docs/specs/13-operations.md#ci.
export default defineConfig({
  testDir: "./tests",
  globalSetup: "./global-setup.ts",
  globalTeardown: "./global-teardown.ts",
  fullyParallel: false,
  workers: 1,
  retries: process.env.CI ? 1 : 0,
  timeout: 60_000,
  expect: { timeout: 10_000 },
  reporter: process.env.CI ? [["list"], ["html", { open: "never" }]] : "list",
  use: {
    baseURL: KMDN_URL,
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
  projects: [
    { name: "desktop", use: { ...devices["Desktop Chrome"] }, testIgnore: [/a11y\.spec\.ts/, /mobile\.spec\.ts/] },
    { name: "a11y", use: { ...devices["Desktop Chrome"] }, testMatch: /a11y\.spec\.ts/, dependencies: ["desktop"] },
    { name: "phone", use: { ...devices["Pixel 7"] }, testMatch: /mobile\.spec\.ts/, dependencies: ["desktop"] },
  ],
});
