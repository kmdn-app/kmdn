import { describe, expect, it } from "vitest";
import { renderHook } from "@testing-library/react";
import { useInitialPanelTab } from "./right-panel";

describe("useInitialPanelTab", () => {
  it("opens on Comments when the page has comments, once it knows", () => {
    const { result, rerender } = renderHook((p: { page: string; ready: boolean; has: boolean }) => useInitialPanelTab(p.page, p.ready, p.has), {
      initialProps: { page: "a.md", ready: false, has: false },
    });
    expect(result.current).toBe("assistant");
    rerender({ page: "a.md", ready: true, has: true });
    expect(result.current).toBe("comments");
    // A comment resolved (or added) later doesn't move the panel.
    rerender({ page: "a.md", ready: true, has: false });
    expect(result.current).toBe("comments");
  });

  it("opens on the Assistant without comments, and decides again per page", () => {
    const { result, rerender } = renderHook((p: { page: string; has: boolean }) => useInitialPanelTab(p.page, true, p.has), {
      initialProps: { page: "a.md", has: false },
    });
    expect(result.current).toBe("assistant");
    rerender({ page: "a.md", has: true });
    expect(result.current).toBe("assistant");
    rerender({ page: "b.md", has: true });
    expect(result.current).toBe("comments");
  });
});
