import { useEffect } from "react";
import { act, fireEvent, render, waitFor } from "@testing-library/react";
import { EditorView } from "@codemirror/view";
import * as Y from "yjs";
import { CONTENT, parse, serialize, readDoc, writeDoc } from "@kmdn/doc-engine";
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
async function sourceEditor(delayDelivery = false) {
  const revision = `revision-${++nextRevision}`;
  const { doc, sourceMap } = parse("Hello\n");
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
  const saved = () => serialize(readDoc(server.getXmlFragment(CONTENT)), sourceMap);
  const deliver = (count = queued.length) => queued.splice(0, count).forEach((update) => Y.applyUpdate(server, update));
  return { ...result, content, view, saved, server, deliver, revision };
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
