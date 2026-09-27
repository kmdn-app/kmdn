import { relative } from "./time";

test("relative", () => {
  const now = Date.parse("2026-09-27T12:00:00Z");
  expect(relative("2026-09-27T11:59:30Z", now)).toBe("just now");
  expect(relative("2026-09-27T11:55:00Z", now)).toBe("5 minutes ago");
  expect(relative("2026-09-26T12:00:00Z", now)).toBe("yesterday");
  expect(relative("2026-05-14T12:00:00Z", now)).toMatch(/May 14, 2026/);
});
