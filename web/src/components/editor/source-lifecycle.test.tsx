import { useEffect } from "react";
import { act, fireEvent, render, waitFor } from "@testing-library/react";
import { EditorView } from "@codemirror/view";
import * as Y from "yjs";
import { CONTENT, SourceSync, parse, writeDoc } from "@kmdn/doc-engine";
import { createApi } from "@kmdn/api-client";
import { sourceBufferMiddleware } from "@/lib/source-buffers";
import { realtime, type RoomProvider } from "@/lib/realtime";
import { vi } from "vitest";
import { SourceEditor } from "./source-editor";

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

beforeAll(() => {
  Object.defineProperty(Range.prototype, "getClientRects", { configurable: true, value: () => [] });
  Object.defineProperty(Range.prototype, "getBoundingClientRect", { configurable: true, value: () => new DOMRect() });
});

let nextRevision = 0;
async function sourceEditor(delayDelivery = false, markdown = "Hello\n", revision = `revision-${++nextRevision}`) {
  const { doc, sourceMap } = parse(markdown);
  const local = new Y.Doc();
  const server = new Y.Doc();
  writeDoc(local.getXmlFragment(CONTENT), doc);
  Y.applyUpdate(server, Y.encodeStateAsUpdate(local));
  const queued: Uint8Array[] = [];
  local.on("update", (update: Uint8Array) => {
    if (delayDelivery) queued.push(update);
    else Y.applyUpdate(server, update);
  });
  vi.stubGlobal("fetch", vi.fn(async () => ({ status: 200, json: async () => sourceMap })));
  const provider = { doc: local, awareness: { setLocalStateField: vi.fn() } } as unknown as RoomProvider;
  function Parent() {
    useEffect(() => () => local.destroy(), []);
    return <SourceEditor provider={provider} revisionID={revision} path="a.md" editable />;
  }
  const result = render(<Parent />);
  await waitFor(() => expect(result.container.querySelector(".cm-content")).not.toBeNull());
  const content = result.container.querySelector(".cm-content") as HTMLElement;
  const view = EditorView.findFromDOM(content)!;
  const saved = () => new SourceSync(server, sourceMap).text();
  const deliver = (count = queued.length) => queued.splice(0, count).forEach((update) => Y.applyUpdate(server, update));
  const receive = (text: string) => {
    new SourceSync(server, sourceMap).apply(text);
    Y.applyUpdate(local, Y.encodeStateAsUpdate(server), "remote");
  };
  return { ...result, content, view, saved, server, deliver, receive, revision };
}

test("flushes the source buffer before the parent destroys the room", async () => {
  const page = await sourceEditor();
  act(() => page.view.dispatch({ changes: { from: 5, insert: " changed" } }));
  expect(page.saved()).toBe("Hello\n");
  page.unmount();
  expect(page.saved()).toBe("Hello changed\n");
  page.server.destroy();
});

test("flushes immediately when focus leaves source mode for Save", async () => {
  const page = await sourceEditor();
  act(() => page.view.dispatch({ changes: { from: 5, insert: " changed" } }));
  fireEvent.blur(page.content);
  expect(page.saved()).toBe("Hello changed\n");
  page.unmount();
  page.server.destroy();
});

test.each(["save", "submit"] as const)("flushes source edits before %s requests without a blur", async (action) => {
  const page = await sourceEditor();
  vi.spyOn(realtime, "waitForUpdates").mockResolvedValue();
  act(() => page.view.dispatch({ changes: { from: 5, insert: " changed" } }));
  const request = vi.fn(async () => {
    expect(page.saved()).toBe("Hello changed\n");
    return Response.json({});
  });
  const client = createApi({ baseUrl: "http://localhost/api/v1", fetch: request });
  client.use(sourceBufferMiddleware);
  await client.POST(`/revisions/{revision}/${action}`, { params: { path: { revision: page.revision } }, body: {} });
  expect(request).toHaveBeenCalledOnce();
  page.unmount();
  page.server.destroy();
});

test("does not send Save until the server acknowledges the source update", async () => {
  const page = await sourceEditor();
  act(() => page.view.dispatch({ changes: { from: 5, insert: " changed" } }));
  let acknowledge!: () => void;
  const barrier = vi.spyOn(realtime, "waitForUpdates").mockImplementation(() => new Promise((resolve) => { acknowledge = resolve; }));
  const request = vi.fn(async () => Response.json({}));
  const client = createApi({ baseUrl: "http://localhost/api/v1", fetch: request });
  client.use(sourceBufferMiddleware);
  const save = client.POST("/revisions/{revision}/save", { params: { path: { revision: page.revision } }, body: {} });
  await waitFor(() => expect(barrier).toHaveBeenCalledOnce());
  expect(request).not.toHaveBeenCalled();
  acknowledge();
  await save;
  expect(request).toHaveBeenCalledOnce();
  page.unmount();
  page.server.destroy();
});

test("does not send Save when the source update loses its connection", async () => {
  const page = await sourceEditor();
  vi.spyOn(realtime, "waitForUpdates").mockRejectedValue(new Error("Disconnected"));
  const request = vi.fn();
  const client = createApi({ baseUrl: "http://localhost/api/v1", fetch: request });
  client.use(sourceBufferMiddleware);
  await expect(client.POST("/revisions/{revision}/save", { params: { path: { revision: page.revision } }, body: {} })).rejects.toThrow("Disconnected");
  expect(request).not.toHaveBeenCalled();
  page.unmount();
  page.server.destroy();
});

test.each(["save", "submit"] as const)("waits for updates flushed during unmount before %s", async (action) => {
  const page = await sourceEditor(true);
  act(() => page.view.dispatch({ changes: { from: 5, insert: " changed" } }));
  page.unmount();
  expect(page.saved()).toBe("Hello\n");
  let acknowledge!: () => void;
  const barrier = vi.spyOn(realtime, "waitForUpdates").mockImplementation(() => new Promise((resolve) => { acknowledge = resolve; }));
  const request = vi.fn(async () => Response.json({}));
  const client = createApi({ baseUrl: "http://localhost/api/v1", fetch: request });
  client.use(sourceBufferMiddleware);
  const saving = client.POST(`/revisions/{revision}/${action}`, { params: { path: { revision: page.revision } }, body: {} });
  await waitFor(() => expect(barrier).toHaveBeenCalledOnce());
  expect(request).not.toHaveBeenCalled();
  page.deliver();
  acknowledge();
  await saving;
  expect(page.saved()).toBe("Hello changed\n");
  expect(request).toHaveBeenCalledOnce();
  page.server.destroy();
});

test("an earlier receipt does not clear a later update flushed during unmount", async () => {
  const page = await sourceEditor(true);
  act(() => page.view.dispatch({ changes: { from: 5, insert: " first" } }));
  const acknowledge: (() => void)[] = [];
  const barrier = vi.spyOn(realtime, "waitForUpdates").mockImplementation(() => new Promise((resolve) => { acknowledge.push(resolve); }));
  const request = vi.fn(async () => Response.json({}));
  const client = createApi({ baseUrl: "http://localhost/api/v1", fetch: request });
  client.use(sourceBufferMiddleware);
  const save = () => client.POST("/revisions/{revision}/save", { params: { path: { revision: page.revision } }, body: {} });
  const first = save();
  await waitFor(() => expect(barrier).toHaveBeenCalledOnce());
  act(() => page.view.dispatch({ changes: { from: 11, insert: " second" } }));
  page.unmount();
  page.deliver(1);
  acknowledge[0]!();
  await first;
  expect(page.saved()).toBe("Hello first\n");
  const second = save();
  await waitFor(() => expect(barrier).toHaveBeenCalledTimes(2));
  expect(request).toHaveBeenCalledOnce();
  page.deliver();
  acknowledge[1]!();
  await second;
  expect(page.saved()).toBe("Hello first second\n");
  expect(request).toHaveBeenCalledTimes(2);
  page.server.destroy();
});

test("source formatting alone reaches the server before Save", async () => {
  const page = await sourceEditor(false, "# Title\n");
  vi.spyOn(realtime, "waitForUpdates").mockResolvedValue();
  act(() => page.view.dispatch({ changes: { from: 0, to: page.view.state.doc.length, insert: "Title\n=====\n" } }));
  const request = vi.fn(async () => Response.json({}));
  const client = createApi({ baseUrl: "http://localhost/api/v1", fetch: request });
  client.use(sourceBufferMiddleware);
  await client.POST("/revisions/{revision}/save", { params: { path: { revision: page.revision } }, body: {} });
  expect(page.saved()).toBe("Title\n=====\n");
  page.unmount();
  page.server.destroy();
});

test("editing source keeps BOM, mixed line endings, and the final newline", async () => {
  const original = "\ufeff# Title\r\n\r\nKeep\n\nLast\r\n";
  const page = await sourceEditor(false, original);
  const from = page.view.state.doc.toString().indexOf("Last") + 4;
  act(() => page.view.dispatch({ changes: { from, insert: " edited" } }));
  fireEvent.blur(page.content);
  expect(page.saved()).toBe(original.replace("Last", "Last edited"));
  page.unmount();
  page.server.destroy();
});

test("an oversized source buffer stays editable and blocks Save even after unmount", async () => {
  const page = await sourceEditor(false, "Hello\n\nRemote\n");
  vi.spyOn(realtime, "waitForUpdates").mockResolvedValue();
  act(() => page.view.dispatch({ changes: { from: 5, insert: "x".repeat((1 << 20) + 1) } }));
  fireEvent.blur(page.content);
  expect(page.getByRole("alert")).toHaveTextContent("too large");
  expect(page.view.state.doc.length).toBeGreaterThan(1 << 20);
  expect(page.saved()).toBe("Hello\n\nRemote\n");
  page.unmount();
  const request = vi.fn(async () => Response.json({}));
  const client = createApi({ baseUrl: "http://localhost/api/v1", fetch: request });
  client.use(sourceBufferMiddleware);
  await expect(client.POST("/revisions/{revision}/save", { params: { path: { revision: page.revision } }, body: {} })).rejects.toThrow("too large");
  expect(request).not.toHaveBeenCalled();
  page.server.destroy();
  const reopened = await sourceEditor(false, "Hello\n\nRemote changed\n", page.revision);
  expect(reopened.view.state.doc.length).toBeGreaterThan(1 << 20);
  expect(reopened.view.state.doc.toString()).toContain("Remote changed");
  expect(reopened.getByRole("alert")).toHaveTextContent("too large");
  await expect(client.POST("/revisions/{revision}/save", { params: { path: { revision: page.revision } }, body: {} })).rejects.toThrow("too large");
  act(() => reopened.view.dispatch({ changes: { from: 0, to: reopened.view.state.doc.length, insert: "Hello recovered\n\nRemote changed\n" } }));
  fireEvent.blur(reopened.content);
  await client.POST("/revisions/{revision}/save", { params: { path: { revision: page.revision } }, body: {} });
  expect(request).toHaveBeenCalledOnce();
  expect(reopened.saved()).toBe("Hello recovered\n\nRemote changed\n");
  reopened.unmount();
  reopened.server.destroy();
});

test.each(["separate local edits", "separate remote edits"])("recovery preserves independent edits: %s", async (mode) => {
  const page = await sourceEditor(false, "First\n\nMiddle\n\nLast\n");
  const huge = "x".repeat((1 << 20) + 1);
  const changes = mode === "separate local edits"
    ? [{ from: 5, insert: huge }, { from: 19, insert: " ours" }]
    : [{ from: 13, insert: huge }];
  act(() => page.view.dispatch({ changes }));
  fireEvent.blur(page.content);
  expect(page.getByRole("alert")).toHaveTextContent("too large");
  page.unmount();
  page.server.destroy();
  const remoteText = mode === "separate local edits"
    ? "First\n\nMiddle theirs\n\nLast\n"
    : "First theirs\n\nMiddle\n\nLast theirs\n";
  const reopened = await sourceEditor(false, remoteText, page.revision);
  const expected = mode === "separate local edits"
    ? "First[LOCAL]\n\nMiddle theirs\n\nLast ours\n"
    : "First theirs\n\nMiddle[LOCAL]\n\nLast theirs\n";
  expect(reopened.view.state.doc.toString().replace(huge, "[LOCAL]")).toBe(expected);
  const start = reopened.view.state.doc.toString().indexOf(huge);
  act(() => reopened.view.dispatch({ changes: { from: start, to: start + huge.length, insert: "[LOCAL]" } }));
  fireEvent.blur(reopened.content);
  expect(reopened.saved()).toBe(expected);
  expect(reopened.queryByRole("alert")).toBeNull();
  reopened.unmount();
  reopened.server.destroy();
});

test("live remote edits around a pending middle edit preserve its position", async () => {
  const page = await sourceEditor(false, "First\n\nMiddle\n\nLast\n");
  act(() => page.view.dispatch({ changes: { from: 13, insert: " ours" } }));
  act(() => page.receive("First theirs\n\nMiddle\n\nLast theirs\n"));
  const expected = "First theirs\n\nMiddle ours\n\nLast theirs\n";
  expect(page.view.state.doc.toString()).toBe(expected);
  expect(page.saved()).toBe(expected);
  page.unmount();
  page.server.destroy();
});

test("a rebase beyond the work limit preserves the draft and blocks Save across remount", async () => {
  const original = "a".repeat(1000) + "\n";
  const remote = "b".repeat(1000) + "\n";
  const page = await sourceEditor(false, original);
  act(() => page.view.dispatch({ changes: { from: 500, insert: " ours" } }));
  act(() => page.receive(remote));
  const draft = original.slice(0, 500) + " ours" + original.slice(500);
  expect(page.view.state.doc.toString()).toBe(draft);
  expect(page.getByRole("alert")).toHaveTextContent("Copy your source");
  expect(page.saved()).toBe(remote);
  const request = vi.fn(async () => Response.json({}));
  const client = createApi({ baseUrl: "http://localhost/api/v1", fetch: request });
  client.use(sourceBufferMiddleware);
  const save = () => client.POST("/revisions/{revision}/save", { params: { path: { revision: page.revision } }, body: {} });
  await expect(save()).rejects.toThrow("could not be combined safely");
  page.unmount();
  page.server.destroy();
  const reopened = await sourceEditor(false, remote, page.revision);
  expect(reopened.view.state.doc.toString()).toBe(draft);
  expect(reopened.getByRole("alert")).toHaveTextContent("Copy your source");
  await expect(save()).rejects.toThrow("could not be combined safely");
  expect(request).not.toHaveBeenCalled();
  reopened.unmount();
  reopened.server.destroy();
});
