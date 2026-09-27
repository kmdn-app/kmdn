import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { vi } from "vitest";
import "@/i18n";
import type { RevisionView } from "@/lib/revisions";
import type { RepoView } from "@/lib/repos";
import type { RoomProvider } from "@/lib/realtime";
import { RevisionPageBody } from "./revision-page";

const mocks = vi.hoisted(() => ({ post: vi.fn(), op: "delete" }));
vi.mock("@/lib/api", async (load) => ({
  ...await load<typeof import("@/lib/api")>(),
  useMe: () => ({ data: { id: "user", name: "Editor" } }),
  api: { POST: mocks.post },
}));
vi.mock("@/lib/revisions", async (load) => ({
  ...await load<typeof import("@/lib/revisions")>(),
  useRevisionContent: () => ({}),
  useRevisionDiff: () => ({}),
  useRevisionFiles: () => ({ data: [{ path: "a.md", op: mocks.op, conflict: "deleted_upstream" }] }),
}));
vi.mock("@/components/consistency/findings", async (load) => ({
  ...await load<typeof import("@/components/consistency/findings")>(),
  useConsistencyMarks: () => ({ findings: [], marks: [] }),
}));

function renderConflict(canEdit = true) {
  const status = { error: { code: "file_deleted", message: "This page is deleted." }, synced: false, online: true, mode: null };
  const retry = vi.fn();
  const provider = { status, retry, onStatus: (callback: (s: typeof status) => void) => { callback(status); return () => {}; } } as unknown as RoomProvider;
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(<QueryClientProvider client={client}><RevisionPageBody
    repo={{ id: "repo" } as RepoView} rev={{ id: "revision", access: { can_edit: canEdit } } as RevisionView}
    path="a.md" provider={provider} onEditor={() => {}} mode="visual" view="result"
    comments={{ active: null, hidden: new Set(), onClick: () => {} }} suggesting={false}
  /></QueryClientProvider>);
  return retry;
}

beforeEach(() => {
  mocks.op = "delete";
  mocks.post.mockReset().mockResolvedValue({ data: {}, response: new Response() });
});

test("a locally deleted page can retain Published's edit and reopen the room", async () => {
  const retry = renderConflict();
  expect(screen.getByText("This revision deleted the page, but Published changed it.")).toBeVisible();
  fireEvent.click(screen.getByRole("button", { name: "Keep Published's page" }));
  await waitFor(() => expect(mocks.post).toHaveBeenCalledWith("/revisions/{revision}/conflicts/resolve", expect.objectContaining({ body: { path: "a.md", choice: "keep" } })));
  await waitFor(() => expect(retry).toHaveBeenCalledOnce());
});

test("a locally deleted page can retain its deletion", async () => {
  const retry = renderConflict();
  fireEvent.click(screen.getByRole("button", { name: "Keep this revision's deletion" }));
  await waitFor(() => expect(mocks.post).toHaveBeenCalledWith("/revisions/{revision}/conflicts/resolve", expect.objectContaining({ body: { path: "a.md", choice: "delete" } })));
  expect(retry).not.toHaveBeenCalled();
});

test("Published-side deletions retain their existing labels", () => {
  mocks.op = "modify";
  renderConflict();
  expect(screen.getByText("Published deleted this page after the revision started changing it.")).toBeVisible();
  expect(screen.getByRole("button", { name: "Keep this revision's page" })).toBeVisible();
});

test("read-only users see the conflict but cannot resolve it", () => {
  renderConflict(false);
  expect(screen.getByText("This revision deleted the page, but Published changed it.")).toBeVisible();
  expect(screen.queryByRole("button")).toBeNull();
});
