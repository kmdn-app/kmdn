/**
 * The tab's single WebSocket (docs/specs/05-collaboration.md#websocket-protocol):
 * frames are [u8 kind][u32 channel][payload]; channels carry y-protocols sync
 * and awareness for one document each, channel 0 carries control and events.
 */
import * as Y from "yjs";
import * as syncProtocol from "y-protocols/sync";
import * as awarenessProtocol from "y-protocols/awareness";
import * as encoding from "lib0/encoding";
import * as decoding from "lib0/decoding";

const CONTROL = 0;
const SYNC = 1;
const AWARENESS = 2;
const EVENT = 3;

export type RealtimeEvent = { scope: string; type: string; [k: string]: unknown };
type Listener = (ev: RealtimeEvent) => void;
export type ConnectionState = "connecting" | "open" | "offline";

type Control = { op: string; channel?: number; scope?: string; mode?: "rw" | "ro"; reason?: string; code?: string; message?: string };

class Connection {
  private ws: WebSocket | null = null;
  private nextChannel = 1;
  private rooms = new Map<number, RoomProvider>();
  private scopes = new Map<string, Set<Listener>>();
  private stateListeners = new Set<(s: ConnectionState) => void>();
  private retry = 0;
  private retryTimer: ReturnType<typeof setTimeout> | null = null;
  private pingTimer: ReturnType<typeof setInterval> | null = null;
  state: ConnectionState = "offline";

  private setState(s: ConnectionState) {
    this.state = s;
    this.stateListeners.forEach((l) => l(s));
  }

  onState(l: (s: ConnectionState) => void): () => void {
    this.stateListeners.add(l);
    return () => {
      this.stateListeners.delete(l);
    };
  }

  private ensure() {
    if (this.ws || this.retryTimer) return;
    const url = `${location.protocol === "https:" ? "wss" : "ws"}://${location.host}/ws`;
    const ws = new WebSocket(url);
    ws.binaryType = "arraybuffer";
    this.ws = ws;
    this.setState("connecting");
    ws.onopen = () => {
      this.retry = 0;
      this.setState("open");
      for (const scope of this.scopes.keys()) this.control({ op: "subscribe-events", scope });
      for (const [ch, room] of this.rooms) room.resubscribe(ch);
      this.pingTimer = setInterval(() => this.control({ op: "ping" }), 20_000);
    };
    ws.onmessage = (e) => this.receive(new Uint8Array(e.data as ArrayBuffer));
    ws.onclose = () => {
      if (this.pingTimer) clearInterval(this.pingTimer);
      this.pingTimer = null;
      this.ws = null;
      for (const room of this.rooms.values()) room.disconnected();
      if (!this.rooms.size && !this.scopes.size) {
        this.setState("offline");
        return;
      }
      this.setState("offline");
      const delay = Math.min(30_000, 1000 * 2 ** this.retry++) * (0.75 + Math.random() / 2);
      this.retryTimer = setTimeout(() => {
        this.retryTimer = null;
        this.ensure();
      }, delay);
    };
  }

  private maybeClose() {
    if (!this.rooms.size && !this.scopes.size && this.ws) {
      const ws = this.ws;
      setTimeout(() => {
        if (!this.rooms.size && !this.scopes.size && this.ws === ws) ws.close();
      }, 5_000);
    }
  }

  send(kind: number, channel: number, payload: Uint8Array) {
    const ws = this.ws;
    if (!ws || ws.readyState !== WebSocket.OPEN) return false;
    const msg = new Uint8Array(5 + payload.length);
    msg[0] = kind;
    new DataView(msg.buffer).setUint32(1, channel);
    msg.set(payload, 5);
    ws.send(msg);
    return true;
  }

  control(v: object) {
    return this.send(CONTROL, 0, new TextEncoder().encode(JSON.stringify(v)));
  }

  private receive(msg: Uint8Array) {
    if (msg.length < 5) return;
    const kind = msg[0]!;
    const channel = new DataView(msg.buffer, msg.byteOffset).getUint32(1);
    const payload = msg.subarray(5);
    if (kind === CONTROL) {
      const c = JSON.parse(new TextDecoder().decode(payload)) as Control;
      if (c.channel) this.rooms.get(c.channel)?.control(c);
      else if (c.op === "error" && c.scope) console.warn("realtime:", c.message);
    } else if (kind === EVENT) {
      const ev = JSON.parse(new TextDecoder().decode(payload)) as RealtimeEvent;
      this.scopes.get(ev.scope)?.forEach((l) => l(ev));
    } else if (kind === SYNC || kind === AWARENESS) {
      this.rooms.get(channel)?.receive(kind, payload);
    }
  }

  /** Follows events for a scope ("repo:<id>", "revision:<id>", "user"). */
  follow(scope: string, l: Listener) {
    let set = this.scopes.get(scope);
    if (!set) {
      set = new Set();
      this.scopes.set(scope, set);
      this.ensure();
      this.control({ op: "subscribe-events", scope });
    }
    set.add(l);
    return () => {
      set!.delete(l);
      if (!set!.size) {
        this.scopes.delete(scope);
        this.control({ op: "unsubscribe-events", scope });
        this.maybeClose();
      }
    };
  }

  open(room: RoomProvider): number {
    const ch = this.nextChannel++;
    this.rooms.set(ch, room);
    this.ensure();
    room.resubscribe(ch);
    return ch;
  }

  leave(ch: number) {
    if (this.rooms.delete(ch)) {
      this.control({ op: "unsubscribe", channel: ch });
      this.maybeClose();
    }
  }
}

export const realtime = new Connection();

export type RoomStatus = {
  /** rw: you can edit; ro: read-only (reason says why); null until subscribed */
  mode: "rw" | "ro" | null;
  reason?: string;
  /** the document has received the server's state */
  synced: boolean;
  /** the room refused us or went away (code: no_document, not_found, deleted…) */
  error?: { code: string; message: string };
  online: boolean;
};

/**
 * A collaborative document on the shared connection. Exposes `doc` and
 * `awareness` like other Yjs providers, so Tiptap's Collaboration and
 * CollaborationCaret extensions can use it.
 */
export class RoomProvider {
  readonly doc = new Y.Doc();
  readonly awareness = new awarenessProtocol.Awareness(this.doc);
  status: RoomStatus = { mode: null, synced: false, online: false };
  private channel = 0;
  private listeners = new Set<(s: RoomStatus) => void>();
  private destroyed = false;

  constructor(readonly revision: string, readonly path: string) {
    this.doc.on("update", (update: Uint8Array, origin: unknown) => {
      if (origin === this || this.status.mode !== "rw") return;
      const e = encoding.createEncoder();
      syncProtocol.writeUpdate(e, update);
      realtime.send(SYNC, this.channel, encoding.toUint8Array(e));
    });
    this.awareness.on("update", ({ added, updated, removed }: { added: number[]; updated: number[]; removed: number[] }, origin: unknown) => {
      if (origin === this) return;
      const changed = [...added, ...updated, ...removed];
      realtime.send(AWARENESS, this.channel, awarenessProtocol.encodeAwarenessUpdate(this.awareness, changed));
    });
    this.channel = realtime.open(this);
  }

  onStatus(l: (s: RoomStatus) => void): () => void {
    this.listeners.add(l);
    l(this.status);
    return () => {
      this.listeners.delete(l);
    };
  }

  private set(patch: Partial<RoomStatus>) {
    this.status = { ...this.status, ...patch };
    this.listeners.forEach((l) => l(this.status));
  }

  /** @internal (re)subscribes after connecting. */
  resubscribe(ch: number) {
    this.channel = ch;
    if (realtime.control({ op: "subscribe", channel: ch, room: { revision: this.revision, path: this.path } })) {
      this.set({ online: true });
    }
  }

  /** @internal */
  disconnected() {
    this.set({ online: false, synced: false });
  }

  /** @internal */
  control(c: Control) {
    switch (c.op) {
      case "subscribed": {
        this.set({ mode: c.mode ?? "ro", reason: c.reason, error: undefined, online: true });
        const e = encoding.createEncoder();
        syncProtocol.writeSyncStep1(e, this.doc);
        realtime.send(SYNC, this.channel, encoding.toUint8Array(e));
        if (this.awareness.getLocalState()) {
          realtime.send(AWARENESS, this.channel, awarenessProtocol.encodeAwarenessUpdate(this.awareness, [this.doc.clientID]));
        }
        break;
      }
      case "mode":
        this.set({ mode: c.mode ?? "ro", reason: c.reason });
        break;
      case "error":
        this.set({ error: { code: c.code ?? "error", message: c.message ?? "" } });
        break;
      case "closed":
        this.set({ error: { code: c.reason ?? "closed", message: "This document closed." }, mode: "ro" });
        break;
    }
  }

  /** @internal */
  receive(kind: number, payload: Uint8Array) {
    if (kind === AWARENESS) {
      awarenessProtocol.applyAwarenessUpdate(this.awareness, payload, this);
      return;
    }
    const d = decoding.createDecoder(payload);
    const e = encoding.createEncoder();
    const type = syncProtocol.readSyncMessage(d, e, this.doc, this);
    if (type === syncProtocol.messageYjsSyncStep2 && !this.status.synced) this.set({ synced: true });
    // Our reply to the server's step 1 carries edits made while offline.
    if (encoding.length(e) > 0 && this.status.mode === "rw") realtime.send(SYNC, this.channel, encoding.toUint8Array(e));
  }

  destroy() {
    if (this.destroyed) return;
    this.destroyed = true;
    awarenessProtocol.removeAwarenessStates(this.awareness, [this.doc.clientID], "local");
    realtime.leave(this.channel);
    this.awareness.destroy();
    this.doc.destroy();
  }
}
