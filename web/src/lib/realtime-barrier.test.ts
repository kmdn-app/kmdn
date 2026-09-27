import { vi } from "vitest";
import { createApi } from "@kmdn/api-client";
import * as Y from "yjs";
import * as encoding from "lib0/encoding";
import * as syncProtocol from "y-protocols/sync";
import { SourceSync } from "@kmdn/doc-engine";
import { realtime, RoomProvider } from "./realtime";
import { markSourceUpdate, sourceBufferMiddleware } from "./source-buffers";

class Socket {
  static OPEN = 1;
  static latest: Socket;
  readyState = 0;
  binaryType = "";
  onopen?: () => void;
  onclose?: () => void;
  onmessage?: (event: { data: ArrayBuffer }) => void;
  send = vi.fn<(frame: Uint8Array) => void>();
  constructor() { Socket.latest = this; }
  open() { this.readyState = 1; this.onopen?.(); }
  close() { this.readyState = 3; this.onclose?.(); }
  control(value: object) {
    const body = new TextEncoder().encode(JSON.stringify(value));
    const frame = new Uint8Array(5 + body.length);
    frame.set(body, 5);
    this.onmessage?.({ data: frame.buffer });
  }
  syncStep1(channel: number) {
    const server = new Y.Doc();
    const encoder = encoding.createEncoder();
    syncProtocol.writeSyncStep1(encoder, server);
    const body = encoding.toUint8Array(encoder);
    const frame = new Uint8Array(5 + body.length);
    frame[0] = 1;
    new DataView(frame.buffer).setUint32(1, channel);
    frame.set(body, 5);
    this.onmessage?.({ data: frame.buffer });
    server.destroy();
  }
}

let stop: () => void;
beforeEach(() => {
  vi.useFakeTimers();
  vi.stubGlobal("WebSocket", Socket);
  stop = realtime.follow("revision:test", () => {});
  Socket.latest.open();
});
afterEach(() => {
  stop();
  vi.advanceTimersByTime(5_000);
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

test("only the matching pong acknowledges queued updates", async () => {
  const received = vi.fn();
  const waiting = realtime.waitForUpdates().then(received);
  const frame = Socket.latest.send.mock.lastCall![0] as Uint8Array;
  const { request_id } = JSON.parse(new TextDecoder().decode(frame.subarray(5))) as { request_id: string };
  Socket.latest.control({ op: "pong" });
  Socket.latest.control({ op: "pong", request_id: "unrelated" });
  await Promise.resolve();
  expect(received).not.toHaveBeenCalled();
  Socket.latest.control({ op: "pong", request_id });
  await waiting;
  expect(received).toHaveBeenCalledOnce();
});

test("rejects an outstanding receipt barrier on disconnect", async () => {
  const waiting = realtime.waitForUpdates();
  const rejected = expect(waiting).rejects.toThrow("connection closed");
  stop();
  Socket.latest.close();
  await rejected;
});

test("rejects a receipt barrier while offline", async () => {
  stop();
  Socket.latest.close();
  await expect(realtime.waitForUpdates()).rejects.toThrow("Reconnect");
});

test("does not acknowledge an update rejected by the server", async () => {
  const waiting = realtime.waitForUpdates();
  const rejected = expect(waiting).rejects.toThrow("Edit refused");
  Socket.latest.control({ op: "error", channel: 1, message: "Edit refused" });
  await rejected;
});

test("rejects a receipt barrier that receives no matching pong", async () => {
  const waiting = realtime.waitForUpdates();
  const rejected = expect(waiting).rejects.toThrow("Timed out");
  vi.advanceTimersByTime(10_000);
  await rejected;
});

function editableRoom(revision: string) {
  const room = new RoomProvider(revision, "a.md");
  const controls = Socket.latest.send.mock.calls.map(([frame]: [Uint8Array]) => frame[0] === 0 ? JSON.parse(new TextDecoder().decode(frame.subarray(5))) as { op: string; channel?: number; room?: { revision: string } } : null);
  const channel = controls.findLast((control) => control?.op === "subscribe" && control.room?.revision === revision)!.channel!;
  Socket.latest.control({ op: "subscribed", channel, mode: "rw" });
  return { room, channel };
}

function acknowledgePings() {
  Socket.latest.send.mockImplementation((frame: Uint8Array) => {
    if (frame[0] !== 0) return;
    const control = JSON.parse(new TextDecoder().decode(frame.subarray(5))) as { op: string; request_id?: string };
    if (control.op === "ping") void Promise.resolve().then(() => Socket.latest.control({ op: "pong", request_id: control.request_id }));
  });
}

test.each([false, true])("a refused paste blocks later Save even after teardown=%s", async (teardown) => {
  const revision = `refused-${teardown}`;
  const { room, channel } = editableRoom(revision);
  room.doc.getText("paste").insert(0, "x".repeat(1_100_000));
  expect(Socket.latest.send.mock.calls.some(([frame]: [Uint8Array]) => frame[0] === 1 && frame.length > 1_048_576)).toBe(true);
  markSourceUpdate(revision);
  if (teardown) room.destroy();
  Socket.latest.control({ op: "error", channel, code: "too_large", message: "That change is too large." });
  acknowledgePings();
  const request = vi.fn(async () => Response.json({}));
  const client = createApi({ baseUrl: "http://localhost/api/v1", fetch: request });
  client.use(sourceBufferMiddleware);
  try {
    await expect(client.POST("/revisions/{revision}/save", { params: { path: { revision } }, body: {} })).rejects.toThrow("too large");
    expect(request).not.toHaveBeenCalled();
  } finally {
    room.destroy();
  }
});

test("a refusal does not block a different revision's updates", async () => {
  const refused = editableRoom("isolated-refusal");
  refused.room.doc.getText("paste").insert(0, "x".repeat(1_100_000));
  Socket.latest.control({ op: "error", channel: refused.channel, code: "too_large", message: "That change is too large." });
  refused.room.destroy();
  const { room } = editableRoom("healthy");
  room.doc.getText("paste").insert(0, "A small edit");
  markSourceUpdate("healthy");
  acknowledgePings();
  const request = vi.fn(async () => Response.json({}));
  const client = createApi({ baseUrl: "http://localhost/api/v1", fetch: request });
  client.use(sourceBufferMiddleware);
  try {
    await client.POST("/revisions/{revision}/save", { params: { path: { revision: "healthy" } }, body: {} });
    expect(request).toHaveBeenCalledOnce();
  } finally {
    room.destroy();
  }
});

test("an earlier pong does not forget a later refused update from a closed room", async () => {
  const revision = "refused-after-earlier-pong";
  const { room, channel } = editableRoom(revision);
  const waiting = realtime.waitForUpdates("unrelated-earlier-request");
  const frame = Socket.latest.send.mock.lastCall![0];
  const { request_id } = JSON.parse(new TextDecoder().decode(frame.subarray(5))) as { request_id: string };
  room.doc.getText("paste").insert(0, "x".repeat(1_100_000));
  markSourceUpdate(revision);
  room.destroy();
  Socket.latest.control({ op: "pong", request_id });
  await waiting;
  Socket.latest.control({ op: "error", channel, code: "too_large", message: "That change is too large." });
  acknowledgePings();
  await expect(realtime.waitForUpdates(revision)).rejects.toThrow("too large");
});

test.each([false, true])("a new connection cannot acknowledge closed-room edits made offline=%s", async (offline) => {
  const revision = `lost-room-${offline}`;
  const { room } = editableRoom(revision);
  if (offline) Socket.latest.close();
  room.doc.getText("paste").insert(0, "Unsaved local edits");
  markSourceUpdate(revision);
  room.destroy();
  if (!offline) Socket.latest.close();
  vi.advanceTimersByTime(1_500);
  Socket.latest.open();
  acknowledgePings();
  const request = vi.fn(async () => Response.json({}));
  const client = createApi({ baseUrl: "http://localhost/api/v1", fetch: request });
  client.use(sourceBufferMiddleware);
  await expect(client.POST("/revisions/{revision}/save", { params: { path: { revision } }, body: {} })).rejects.toThrow(/Copy.*before reloading/);
  expect(request).not.toHaveBeenCalled();
});

test("an open document must resend offline edits before a new connection can acknowledge them", async () => {
  const revision = "resync-live-room";
  const { room, channel } = editableRoom(revision);
  Socket.latest.close();
  room.doc.getText("paste").insert(0, "Offline edits");
  markSourceUpdate(revision);
  vi.advanceTimersByTime(1_500);
  Socket.latest.open();
  acknowledgePings();
  try {
    await expect(realtime.waitForUpdates(revision)).rejects.toThrow("synchronizing");
    Socket.latest.control({ op: "subscribed", channel, mode: "rw" });
    Socket.latest.syncStep1(channel);
    await expect(realtime.waitForUpdates(revision)).resolves.toBeUndefined();
  } finally {
    room.destroy();
  }
});

test("a source flush after becoming read-only remains unsaved when editing returns", async () => {
  const revision = "readonly-source-flush";
  const { room, channel } = editableRoom(revision);
  const source = new SourceSync(room.doc);
  Socket.latest.control({ op: "mode", channel, mode: "ro", reason: "in_review" });
  const sent = Socket.latest.send.mock.calls.length;
  source.apply("Buffered source edits\n");
  markSourceUpdate(revision);
  expect(Socket.latest.send).toHaveBeenCalledTimes(sent);
  Socket.latest.control({ op: "mode", channel, mode: "rw" });
  acknowledgePings();
  const request = vi.fn(async () => Response.json({}));
  const client = createApi({ baseUrl: "http://localhost/api/v1", fetch: request });
  client.use(sourceBufferMiddleware);
  try {
    await expect(client.POST("/revisions/{revision}/save", { params: { path: { revision } }, body: {} })).rejects.toThrow("read-only");
    expect(request).not.toHaveBeenCalled();
  } finally {
    room.destroy();
  }
});

test("Save waits for visual editor updates when no Source editor was opened", async () => {
  const revision = "visual-editor-receipt";
  const { room } = editableRoom(revision);
  room.doc.getText("visual").insert(0, "Visual editor edit");
  const request = vi.fn(async () => Response.json({}));
  const client = createApi({ baseUrl: "http://localhost/api/v1", fetch: request });
  client.use(sourceBufferMiddleware);
  try {
    const saving = client.POST("/revisions/{revision}/save", { params: { path: { revision } }, body: {} });
    await Promise.resolve();
    expect(request).not.toHaveBeenCalled();
    const frame = Socket.latest.send.mock.lastCall![0];
    expect(frame[0]).toBe(0);
    const { op, request_id } = JSON.parse(new TextDecoder().decode(frame.subarray(5))) as { op: string; request_id: string };
    expect(op).toBe("ping");
    Socket.latest.control({ op: "pong", request_id });
    await saving;
    expect(request).toHaveBeenCalledOnce();
  } finally {
    room.destroy();
  }
});
