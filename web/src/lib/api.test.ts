import { describe, expect, it } from "vitest";
import { ApiError, retryDelay, retryQuery } from "./api";

const apiError = (status: number) => new ApiError({ type: "about:blank", title: "", status, code: "http_" + status });

describe("retryQuery", () => {
  it("waits out a restart", () => {
    for (const e of [apiError(502), apiError(503), apiError(504), new TypeError("Failed to fetch")]) {
      expect(retryQuery(5, e)).toBe(true);
      expect(retryQuery(6, e)).toBe(false);
    }
    const waited = [0, 1, 2, 3, 4, 5].reduce((ms, n) => ms + retryDelay(n), 0);
    expect(waited).toBeGreaterThanOrEqual(15_000);
  });

  it("retries other failures once", () => {
    for (const e of [apiError(500), apiError(404), new Error("x")]) {
      expect(retryQuery(0, e)).toBe(true);
      expect(retryQuery(1, e)).toBe(false);
    }
  });
});
