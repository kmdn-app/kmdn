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
import type * as Y from "yjs";
import setup from "../global-setup.ts";
import teardown from "../global-teardown.ts";
import { STATE_FILE, type State } from "../env.ts";
import { Editor, api, metric, ms, pct, summary, timeGet } from "./lib.ts";
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
    "GET revisions (open)": summary(await timeGet(st, `/repos/${st.repoID}/revisions?state=open`)),
    "GET revisions (mine, the picker)": summary(await timeGet(st, `/repos/${st.repoID}/revisions?mine=true`)),
    "GET a revision": summary(await timeGet(st, `/revisions/${first}`)),
    "GET repo tree": summary(await timeGet(st, `/repos/${st.repoID}/tree`)),
    "GET inbox": summary(await timeGet(st, `/notifications`)),
    "GET search": summary(await timeGet(st, `/repos/${st.repoID}/search?q=travel`)),
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
