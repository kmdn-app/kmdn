// Pieces the load scripts share: API calls, metrics, percentiles and a Yjs
// editor that speaks the room protocol over a WebSocket.
import { performance } from "node:perf_hooks";
import WebSocket from "ws";
import * as Y from "yjs";
import * as syncProtocol from "y-protocols/sync";
import * as encoding from "lib0/encoding";
import * as decoding from "lib0/decoding";
import { KMDN_URL, type State } from "../env.ts";

const CONTROL = 0;
const SYNC = 1;

export function pct(xs: number[], p: number): number {
  if (!xs.length) return NaN;
  const s = [...xs].sort((a, b) => a - b);
  return s[Math.min(s.length - 1, Math.floor((p / 100) * s.length))]!;
}
export const ms = (x: number) => `${x.toFixed(0)} ms`;

export async function api(st: State, method: string, path: string, body?: unknown) {
  const res = await fetch(KMDN_URL + "/api/v1" + path, {
    method,
    headers: { "Content-Type": "application/json", Cookie: st.admin.cookie, "X-Kmdn-CSRF": st.admin.csrf },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await res.text();
  if (!res.ok) throw new Error(`${method} ${path}: ${res.status} ${text}`);
  return text ? JSON.parse(text) : null;
}

export async function metric(name: string): Promise<number> {
  const text = await (await fetch(KMDN_URL + "/metrics")).text();
  const line = text.split("\n").find((l) => l.startsWith(name + " "));
  return line ? Number(line.split(" ")[1]) : NaN;
}

/** One editor: a Yjs doc synced with the page's room over its own socket. */
export class Editor {
  doc = new Y.Doc();
  ws: WebSocket;
  synced: Promise<void>;
  errors = 0;
  private ready!: () => void;

  /** Times the socket reopened after the server went away. */
  reconnects = 0;
  private closing = false;

  private st: State;
  private revision: string;
  private path: string;

  constructor(st: State, revision: string, path: string, onUpdate?: (doc: Y.Doc) => void) {
    this.st = st;
    this.revision = revision;
    this.path = path;
    this.synced = new Promise((r) => (this.ready = r));
    this.doc.on("update", (u: Uint8Array, origin: unknown) => {
      if (origin !== this) {
        const e = encoding.createEncoder();
        syncProtocol.writeUpdate(e, u);
        this.send(SYNC, encoding.toUint8Array(e));
      }
      onUpdate?.(this.doc);
    });
    this.ws = this.connect();
  }

  /** Opens the socket; after a drop it reconnects, resyncs and resends what it has. */
  private connect(): WebSocket {
    const ws = new WebSocket(KMDN_URL.replace("http", "ws") + "/ws", { headers: { Cookie: this.st.admin.cookie, Origin: KMDN_URL } });
    ws.binaryType = "arraybuffer";
    ws.on("open", () => this.control({ op: "subscribe", channel: 1, room: { revision: this.revision, path: this.path } }));
    ws.on("error", () => {
      if (!this.closing) this.errors++;
    });
    ws.on("close", () => {
      if (this.closing) return;
      this.reconnects++;
      setTimeout(() => {
        if (!this.closing) this.ws = this.connect();
      }, 250);
    });
    ws.on("message", (data: ArrayBuffer) => {
      const msg = new Uint8Array(data);
      const kind = msg[0]!;
      const payload = msg.subarray(5);
      if (kind === CONTROL) {
        const c = JSON.parse(new TextDecoder().decode(payload));
        if (c.op === "subscribed") {
          const e = encoding.createEncoder();
          syncProtocol.writeSyncStep1(e, this.doc);
          this.send(SYNC, encoding.toUint8Array(e));
          if (this.reconnects > 0) {
            // Edits made while away: Yjs ignores what the server has already.
            const u = encoding.createEncoder();
            syncProtocol.writeUpdate(u, Y.encodeStateAsUpdate(this.doc));
            this.send(SYNC, encoding.toUint8Array(u));
          }
        } else if (c.op === "error") this.errors++;
        return;
      }
      if (kind !== SYNC) return;
      const d = decoding.createDecoder(payload);
      const e = encoding.createEncoder();
      const type = syncProtocol.readSyncMessage(d, e, this.doc, this);
      if (encoding.length(e) > 0) this.send(SYNC, encoding.toUint8Array(e));
      if (type === syncProtocol.messageYjsSyncStep2) this.ready();
    });
    return ws;
  }

  send(kind: number, payload: Uint8Array) {
    if (this.ws.readyState !== WebSocket.OPEN) return;
    const msg = new Uint8Array(5 + payload.length);
    msg[0] = kind;
    new DataView(msg.buffer).setUint32(1, 1);
    msg.set(payload, 5);
    this.ws.send(msg);
  }

  control(v: unknown) {
    if (this.ws.readyState !== WebSocket.OPEN) return;
    const p = new TextEncoder().encode(JSON.stringify(v));
    const msg = new Uint8Array(5 + p.length);
    msg[0] = CONTROL;
    msg.set(p, 5);
    this.ws.send(msg);
  }

  /** Appends text to the page's first paragraph. */
  type(text: string) {
    const frag = this.doc.getXmlFragment("content");
    for (const n of frag.toArray()) {
      if (n instanceof Y.XmlElement && n.nodeName === "paragraph") {
        const t = n.toArray().find((c) => c instanceof Y.XmlText) as Y.XmlText | undefined;
        if (t) {
          t.insert(t.length, text);
          return;
        }
      }
    }
    throw new Error("no paragraph to type in");
  }

  text(): string {
    return this.doc.getXmlFragment("content").toString();
  }

  close() {
    this.closing = true;
    this.ws.close();
    this.doc.destroy();
  }
}


/** Times GET path n times, one after another. */
export async function timeGet(st: State, path: string, samples = 20): Promise<number[]> {
  const xs: number[] = [];
  for (let i = 0; i < samples; i++) {
    const t = performance.now();
    await api(st, "GET", path);
    xs.push(performance.now() - t);
  }
  return xs;
}

/** "p50 x ms, p95 y ms, p99 z ms". */
export const summary = (xs: number[]) => `p50 ${ms(pct(xs, 50))}, p95 ${ms(pct(xs, 95))}, p99 ${ms(pct(xs, 99))}`;
