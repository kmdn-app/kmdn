// Load test (docs/specs/14-roadmap.md#top-risks, issue #70): many editors on
// one page, and many open revisions. Starts the same stack as the e2e suite
// (bin/kmdn + the fake forge) and prints a report.
//
//   node e2e/load/load.ts [--editors 50] [--seconds 30] [--interval 1000] [--revisions 500]
//
// Every editor is a real WebSocket client speaking the room protocol with
// Yjs; they share the admin's session (presence dedupes by person, rooms
// don't).
import { performance } from "node:perf_hooks";
import { parseArgs } from "node:util";
import WebSocket from "ws";
import * as Y from "yjs";
import * as syncProtocol from "y-protocols/sync";
import * as encoding from "lib0/encoding";
import * as decoding from "lib0/decoding";
import setup from "../global-setup.ts";
import teardown from "../global-teardown.ts";
import { KMDN_URL, STATE_FILE, type State } from "../env.ts";
import { readFileSync } from "node:fs";

const { values: opts } = parseArgs({
  options: {
    editors: { type: "string", default: "50" },
    seconds: { type: "string", default: "30" },
    interval: { type: "string", default: "1000" },
    revisions: { type: "string", default: "500" },
  },
});
const EDITORS = Number(opts.editors);
const SECONDS = Number(opts.seconds);
const INTERVAL = Number(opts.interval);
const REVISIONS = Number(opts.revisions);

const CONTROL = 0;
const SYNC = 1;

function pct(xs: number[], p: number): number {
  if (!xs.length) return NaN;
  const s = [...xs].sort((a, b) => a - b);
  return s[Math.min(s.length - 1, Math.floor((p / 100) * s.length))]!;
}
const ms = (x: number) => `${x.toFixed(0)} ms`;

async function api(st: State, method: string, path: string, body?: unknown) {
  const res = await fetch(KMDN_URL + "/api/v1" + path, {
    method,
    headers: { "Content-Type": "application/json", Cookie: st.admin.cookie, "X-Kmdn-CSRF": st.admin.csrf },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await res.text();
  if (!res.ok) throw new Error(`${method} ${path}: ${res.status} ${text}`);
  return text ? JSON.parse(text) : null;
}

async function metric(name: string): Promise<number> {
  const text = await (await fetch(KMDN_URL + "/metrics")).text();
  const line = text.split("\n").find((l) => l.startsWith(name + " "));
  return line ? Number(line.split(" ")[1]) : NaN;
}

/** One editor: a Yjs doc synced with the page's room over its own socket. */
class Editor {
  doc = new Y.Doc();
  ws: WebSocket;
  synced: Promise<void>;
  errors = 0;
  private ready!: () => void;

  constructor(st: State, revision: string, path: string, onUpdate?: (doc: Y.Doc) => void) {
    this.synced = new Promise((r) => (this.ready = r));
    this.ws = new WebSocket(KMDN_URL.replace("http", "ws") + "/ws", { headers: { Cookie: st.admin.cookie, Origin: KMDN_URL } });
    this.ws.binaryType = "arraybuffer";
    this.doc.on("update", (u: Uint8Array, origin: unknown) => {
      if (origin !== this) {
        const e = encoding.createEncoder();
        syncProtocol.writeUpdate(e, u);
        this.send(SYNC, encoding.toUint8Array(e));
      }
      onUpdate?.(this.doc);
    });
    this.ws.on("open", () => this.control({ op: "subscribe", channel: 1, room: { revision, path } }));
    this.ws.on("error", () => this.errors++);
    this.ws.on("message", (data: ArrayBuffer) => {
      const msg = new Uint8Array(data);
      const kind = msg[0]!;
      const payload = msg.subarray(5);
      if (kind === CONTROL) {
        const c = JSON.parse(new TextDecoder().decode(payload));
        if (c.op === "subscribed") {
          const e = encoding.createEncoder();
          syncProtocol.writeSyncStep1(e, this.doc);
          this.send(SYNC, encoding.toUint8Array(e));
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
    this.ws.close();
    this.doc.destroy();
  }
}

async function editorsPhase(st: State) {
  const rev = await api(st, "POST", `/repos/${st.repoID}/revisions`, { title: "Load: many editors", path: "docs/index.md" });
  const sent = new Map<string, number>();
  const latencies: number[] = [];
  const seen = new Set<string>();
  const token = /\[c\d+\.\d+\]/g;
  // Editor 0 observes: the time from a token being typed elsewhere to it
  // arriving here is the edit round trip (client → server → peer).
  const observe = (doc: Y.Doc) => {
    const text = doc.getXmlFragment("content").toString();
    for (const m of text.matchAll(token)) {
      if (seen.has(m[0])) continue;
      seen.add(m[0]);
      const t0 = sent.get(m[0]);
      if (t0 !== undefined) latencies.push(performance.now() - t0);
    }
  };
  const t0 = performance.now();
  const editors = Array.from({ length: EDITORS }, (_, i) => new Editor(st, rev.id, "docs/index.md", i === 0 ? observe : undefined));
  await Promise.all(editors.map((e) => e.synced));
  const connectMs = performance.now() - t0;
  const cpu0 = await metric("process_cpu_seconds_total");
  let n = 0;
  const timers = editors.slice(1).map((e, i) =>
    setInterval(
      () => {
        const tok = `[c${i + 1}.${n++}]`;
        sent.set(tok, performance.now());
        e.type(tok);
      },
      INTERVAL + ((i * 37) % 200), // spread them out
    ),
  );
  await new Promise((r) => setTimeout(r, SECONDS * 1000));
  timers.forEach(clearInterval);
  // Let everything arrive, then compare every copy.
  let converged = false;
  for (let i = 0; i < 50 && !converged; i++) {
    await new Promise((r) => setTimeout(r, 200));
    const want = editors[0]!.text();
    converged = editors.every((e) => e.text() === want);
  }
  const cpu1 = await metric("process_cpu_seconds_total");
  const rss = await metric("go_memstats_heap_inuse_bytes");
  const errors = editors.reduce((s, e) => s + e.errors, 0);
  editors.forEach((e) => e.close());
  // The server materializes the page: every token is in the stored markdown.
  await new Promise((r) => setTimeout(r, 3000));
  const files = await api(st, "GET", `/revisions/${rev.id}/files/docs/index.md`).catch(() => null);
  const stored = typeof files?.content === "string" ? [...files.content.matchAll(token)].length : NaN;
  return {
    editors: EDITORS,
    seconds: SECONDS,
    typed: sent.size,
    received: latencies.length,
    connect: ms(connectMs),
    p50: ms(pct(latencies, 50)),
    p95: ms(pct(latencies, 95)),
    p99: ms(pct(latencies, 99)),
    max: ms(pct(latencies, 100)),
    converged,
    materialized: stored,
    errors,
    cpuPerSecond: ((cpu1 - cpu0) / SECONDS).toFixed(2),
    heapMB: (rss / 1e6).toFixed(0),
  };
}

async function timeGet(st: State, path: string, samples = 20) {
  const xs: number[] = [];
  for (let i = 0; i < samples; i++) {
    const t = performance.now();
    await api(st, "GET", path);
    xs.push(performance.now() - t);
  }
  return `p50 ${ms(pct(xs, 50))}, p95 ${ms(pct(xs, 95))}`;
}

async function revisionsPhase(st: State) {
  const t0 = performance.now();
  let first = "";
  for (let i = 0; i < REVISIONS; i++) {
    const r = await api(st, "POST", `/repos/${st.repoID}/revisions`, { title: `Load revision ${i + 1}`, path: i % 2 ? "docs/travel.md" : "docs/index.md" });
    if (!first) first = r.id;
  }
  const createMs = performance.now() - t0;
  return {
    created: REVISIONS,
    createPerSecond: (REVISIONS / (createMs / 1000)).toFixed(0),
    "GET revisions (open)": await timeGet(st, `/repos/${st.repoID}/revisions?state=open`),
    "GET revisions (mine, the picker)": await timeGet(st, `/repos/${st.repoID}/revisions?mine=true`),
    "GET a revision": await timeGet(st, `/revisions/${first}`),
    "GET repo tree": await timeGet(st, `/repos/${st.repoID}/tree`),
    "GET inbox": await timeGet(st, `/notifications`),
    "GET search": await timeGet(st, `/repos/${st.repoID}/search?q=travel`),
  };
}

await setup();
const st = JSON.parse(readFileSync(STATE_FILE, "utf8")) as State;
try {
  console.log(`## ${EDITORS} editors on one page for ${SECONDS}s (one keystroke burst each per ~${INTERVAL} ms)`);
  console.table(await editorsPhase(st));
  console.log(`## ${REVISIONS} open revisions`);
  console.table(await revisionsPhase(st));
} finally {
  await teardown();
}
