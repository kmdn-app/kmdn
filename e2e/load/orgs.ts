// Multi-org load test (docs/specs/13-operations.md#capacity): many orgs on
// one process, some of them busy at once, one of them noisy, and a restart
// in the middle of editing. Starts the same stack as the e2e suite (bin/kmdn
// + the fake forge) in orgs.mode multi and prints a report with p50/p95/p99.
//
//   node e2e/load/orgs.ts [--orgs 200] [--active 20] [--editors 20] [--seconds 30]
//                         [--interval 1000] [--revisions 5000] [--big 2000] [--restart] [--scan]
//
// KMDN_E2E_DB_URL=postgres://… runs it on Postgres (a fresh database); run it
// on the production host size to size the host. --scan also runs a
// consistency scan in the noisy org (needs an assistant provider configured
// through KMDN_ASSISTANT_*).
import { performance } from "node:perf_hooks";
import { parseArgs } from "node:util";
import { readFileSync } from "node:fs";
import * as Y from "yjs";
import setup, { restartKmdn } from "../global-setup.ts";
import teardown from "../global-teardown.ts";
import { FORGE_TOKEN, FORGE_URL, STATE_FILE, type State } from "../env.ts";
import { Editor, api, metric, ms, pct, summary, timeGet } from "./lib.ts";

const { values: opts } = parseArgs({
  options: {
    orgs: { type: "string", default: "200" },
    active: { type: "string", default: "20" },
    editors: { type: "string", default: "20" },
    seconds: { type: "string", default: "30" },
    interval: { type: "string", default: "1000" },
    revisions: { type: "string", default: "5000" },
    big: { type: "string", default: "2000" },
    restart: { type: "boolean", default: false },
    scan: { type: "boolean", default: false },
  },
});
const ORGS = Number(opts.orgs);
const ACTIVE = Math.min(Number(opts.active), ORGS);
const EDITORS = Number(opts.editors);
const SECONDS = Number(opts.seconds);
const INTERVAL = Number(opts.interval);
const REVISIONS = Number(opts.revisions);
const BIG = Number(opts.big);

process.env.KMDN_ORGS_MODE = "multi";

async function waitSynced(st: State, repoID: string, seconds = 120) {
  for (let i = 0; i < seconds * 5; i++) {
    const r = await api(st, "GET", `/repos/${repoID}`);
    if (r.head_sha) return;
    await new Promise((r) => setTimeout(r, 200));
  }
  throw new Error(`repo ${repoID} never synced`);
}

async function seed(path: string, files: Record<string, string>) {
  const res = await fetch(FORGE_URL + "/_fake/projects", { method: "POST", body: JSON.stringify({ path, files }) });
  if (!res.ok) throw new Error(`seed ${path}: ${await res.text()}`);
}

async function connect(st: State, org: string, owner: string, name: string): Promise<string> {
  const c = await api(st, "POST", `/orgs/${org}/repos`, { forge_host_id: st.forgeHost, owner, name, token: FORGE_TOKEN, content_root: "docs/" });
  return c.repo.id as string;
}

/** 200 orgs, and a repo in each active one. */
async function orgsPhase(st: State) {
  if (BIG > 0) {
    const files: Record<string, string> = {};
    for (let i = 0; i < BIG; i++) files[`docs/section-${i % 40}/page-${i}.md`] = `# Page ${i}\n\n${"Some policy text that links to [the index](../../index.md). ".repeat(20)}\n`;
    await seed("acme/big", files);
  }
  const t0 = performance.now();
  const slugs: string[] = [];
  for (let i = 1; i <= ORGS; i++) {
    const o = await api(st, "POST", "/orgs", { name: `Load ${i}`, slug: `load-${i}` });
    slugs.push(o.slug);
  }
  const createMs = performance.now() - t0;
  const repos: string[] = [];
  for (const slug of slugs.slice(0, ACTIVE)) repos.push(await connect(st, slug, "acme", "handbook"));
  await Promise.all(repos.map((r) => waitSynced(st, r)));
  return { slugs, repos, report: { orgs: ORGS, createPerSecond: (ORGS / (createMs / 1000)).toFixed(0), activeWithRepos: ACTIVE } };
}

/** EDITORS editors in each active org at once, a noisy import alongside, maybe a restart. */
async function editingPhase(st: State, active: { slug: string; repo: string }[]) {
  const token = /\[o\d+e\d+\.\d+\]/g;
  const sent = new Map<string, number>();
  const latencies: number[] = [];
  let downFrom = Infinity;
  let downTo = -Infinity;
  const rooms = await Promise.all(
    active.map(async ({ repo }, o) => {
      const rev = await api(st, "POST", `/repos/${repo}/revisions`, { title: "Load: editors", path: "docs/index.md" });
      // Editor 0 of each org observes the others' tokens arriving, from the
      // inserted text only (rescanning the page per update would load the
      // client, not the server).
      const observe = (inserted: string) => {
        for (const m of inserted.matchAll(token)) {
          const t = sent.get(m[0]);
          // Tokens typed while the server was down measure the outage, not latency.
          if (t !== undefined && (t < downFrom || t > downTo)) latencies.push(performance.now() - t);
        }
      };
      const editors = Array.from({ length: EDITORS }, () => new Editor(st, rev.id, "docs/index.md"));
      editors[0]!.doc.getXmlFragment("content").observeDeep((events) => {
        for (const ev of events) {
          if (!(ev.target instanceof Y.XmlText)) continue;
          for (const d of ev.delta) if (typeof d.insert === "string") observe(d.insert);
        }
      });
      await Promise.all(editors.map((e) => e.synced));
      return { rev: rev.id as string, editors, org: o };
    }),
  );
  const cpu0 = await metric("process_cpu_seconds_total");

  // The noisy neighbour: a big repo imports in its own org while everyone types.
  let noisy: Promise<number> = Promise.resolve(NaN);
  if (BIG > 0) {
    await api(st, "POST", "/orgs", { name: "Noisy", slug: "load-noisy" });
    const noisyStart = performance.now();
    const bigRepo = await connect(st, "load-noisy", "acme", "big");
    noisy = waitSynced(st, bigRepo, 600).then(async () => {
      const importMs = performance.now() - noisyStart;
      if (opts.scan) await api(st, "POST", `/repos/${bigRepo}/consistency/scan`, {});
      return importMs;
    });
  }

  let n = 0;
  const timers = rooms.flatMap(({ editors, org }) =>
    editors.slice(1).map((e, i) =>
      setInterval(
        () => {
          const tok = `[o${org}e${i + 1}.${n++}]`;
          sent.set(tok, performance.now());
          e.type(tok);
        },
        INTERVAL + ((i * 37 + org * 11) % 200),
      ),
    ),
  );
  let restartMs = NaN;
  if (opts.restart) {
    await new Promise((r) => setTimeout(r, (SECONDS * 1000) / 2));
    downFrom = performance.now();
    Object.assign(st, await restartKmdn(st));
    downTo = performance.now();
    restartMs = downTo - downFrom;
    await new Promise((r) => setTimeout(r, (SECONDS * 1000) / 2));
  } else {
    await new Promise((r) => setTimeout(r, SECONDS * 1000));
  }
  timers.forEach(clearInterval);
  const importMs = await noisy;

  // Every copy converges, and every token typed reached the stored markdown.
  let converged = false;
  for (let i = 0; i < 100 && !converged; i++) {
    await new Promise((r) => setTimeout(r, 200));
    converged = rooms.every(({ editors }) => editors.every((e) => e.text() === editors[0]!.text()));
  }
  const cpu1 = await metric("process_cpu_seconds_total");
  const heap = await metric("go_memstats_heap_inuse_bytes");
  const errors = rooms.reduce((s, r) => s + r.editors.reduce((t, e) => t + e.errors, 0), 0);
  const reconnects = rooms.reduce((s, r) => s + r.editors.reduce((t, e) => t + e.reconnects, 0), 0);
  rooms.forEach(({ editors }) => editors.forEach((e) => e.close()));
  await new Promise((r) => setTimeout(r, 3000));
  let stored = 0;
  for (const { rev } of rooms) {
    const f = await api(st, "GET", `/revisions/${rev}/files/docs/index.md`).catch(() => null);
    if (typeof f?.content === "string") stored += [...f.content.matchAll(token)].length;
  }
  return {
    orgs: rooms.length,
    editorsPerOrg: EDITORS,
    seconds: SECONDS,
    typed: sent.size,
    materialized: stored,
    lost: sent.size - stored,
    converged,
    p50: ms(pct(latencies, 50)),
    p95: ms(pct(latencies, 95)),
    p99: ms(pct(latencies, 99)),
    max: ms(pct(latencies, 100)),
    restart: opts.restart ? ms(restartMs) : "no",
    reconnects,
    errors,
    noisyImport: BIG > 0 ? `${BIG} files, synced in ${ms(importMs)}${opts.scan ? " + scan" : ""}` : "none",
    cpuPerSecond: ((cpu1 - cpu0) / SECONDS).toFixed(2),
    heapMB: (heap / 1e6).toFixed(0),
  };
}

/** REVISIONS open revisions across the active orgs; list, search and inbox latency. */
async function revisionsPhase(st: State, active: { slug: string; repo: string }[]) {
  const t0 = performance.now();
  const per = Math.ceil(REVISIONS / active.length);
  let first = "";
  // A few orgs at a time, as people in several orgs would.
  await Promise.all(
    active.map(async ({ repo }) => {
      for (let i = 0; i < per; i++) {
        const r = await api(st, "POST", `/repos/${repo}/revisions`, { title: `Load revision ${i + 1}`, path: i % 2 ? "docs/travel.md" : "docs/index.md" });
        if (!first) first = r.id;
      }
    }),
  );
  const createMs = performance.now() - t0;
  const sample = active.slice(0, Math.min(5, active.length));
  const each = async (f: (a: { slug: string; repo: string }) => string) => {
    const xs: number[] = [];
    for (const a of sample) xs.push(...(await timeGet(st, f(a), 20)));
    return summary(xs);
  };
  return {
    created: per * active.length,
    createPerSecond: ((per * active.length) / (createMs / 1000)).toFixed(0),
    "GET revisions (open)": await each((a) => `/repos/${a.repo}/revisions?state=open`),
    "GET revisions (mine)": await each((a) => `/repos/${a.repo}/revisions?mine=true`),
    "GET a revision": summary(await timeGet(st, `/revisions/${first}`, 100)),
    "GET org repos": await each((a) => `/orgs/${a.slug}/repos`),
    "GET search": await each((a) => `/repos/${a.repo}/search?q=travel`),
    "GET inbox": summary(await timeGet(st, `/notifications`, 100)),
  };
}

await setup();
let st = JSON.parse(readFileSync(STATE_FILE, "utf8")) as State;
try {
  console.log(`## ${ORGS} orgs, ${ACTIVE} with a repo`);
  const { slugs, repos, report } = await orgsPhase(st);
  console.table(report);
  const active = slugs.slice(0, ACTIVE).map((slug, i) => ({ slug, repo: repos[i]! }));
  console.log(`## ${ACTIVE} orgs × ${EDITORS} editors for ${SECONDS}s, a ${BIG}-file import in another org${opts.restart ? ", a restart halfway" : ""}`);
  console.table(await editingPhase(st, active));
  st = JSON.parse(readFileSync(STATE_FILE, "utf8")) as State;
  console.log(`## ${REVISIONS} open revisions across ${ACTIVE} orgs`);
  console.table(await revisionsPhase(st, active));
} finally {
  await teardown();
}
