import * as Y from "yjs";
import { canonical } from "./hash";
import { parse } from "./parse";
import { SourceSync, textChange } from "./source-sync";
import { CONTENT, writeDoc } from "./ydoc";

/** A server-made document, like yFromMarkdown. */
function serverDoc(md: string) {
  const { doc, sourceMap } = parse(md);
  const y = new Y.Doc();
  writeDoc(y.getXmlFragment(CONTENT), doc);
  return { update: Y.encodeStateAsUpdate(y), sourceMap };
}

function replica(update: Uint8Array) {
  const d = new Y.Doc();
  Y.applyUpdate(d, update);
  return d;
}

const ORIGINAL = "---\ntitle: T\n---\n\n# Title\n\nKeep   *odd* spacing.\n\n* star bullets\n* stay\n\nLast para.\n";

test("shows the original bytes and round-trips its own edits exactly", () => {
  const { update, sourceMap } = serverDoc(ORIGINAL);
  const s = new SourceSync(replica(update), sourceMap);
  expect(s.text()).toBe(ORIGINAL);
  const edited = ORIGINAL.replace("Last para.", "Last   para, *edited*_x_.");
  s.apply(edited);
  expect(s.text()).toBe(edited);
});

test("a collaborator's edit re-serializes only their block", () => {
  const { update, sourceMap } = serverDoc(ORIGINAL);
  const a = new SourceSync(replica(update), sourceMap);
  const b = new SourceSync(replica(update), sourceMap);
  a.doc.on("update", (u: Uint8Array, o: unknown) => o !== "remote" && Y.applyUpdate(b.doc, u, "remote"));
  b.doc.on("update", (u: Uint8Array, o: unknown) => o !== "remote" && Y.applyUpdate(a.doc, u, "remote"));
  // A writes non-canonical markdown in one block; B edits another block.
  a.apply(ORIGINAL.replace("# Title", "Title\n=====").replace("Last para.", "Last   para."));
  b.apply(b.text().replace("* stay", "* stay put"));
  // A still sees its own bytes; only B's block changed (and keeps its * bullets
  // since B edited it from the text it had).
  expect(a.text()).toBe(ORIGINAL.replace("# Title", "Title\n=====").replace("Last para.", "Last   para.").replace("* stay", "* stay put"));
});

test("textChange finds the minimal edit", () => {
  expect(textChange("hello world", "hello brave world")).toEqual({ from: 6, to: 6, insert: "brave " });
  expect(textChange("abc", "abc")).toBeNull();
  expect(textChange("aXc", "aYc")).toEqual({ from: 1, to: 2, insert: "Y" });
});

// Spike S3: three source-mode clients editing concurrently converge, and
// each client's text is stable (re-applying it changes nothing).
function fuzz(seedStart: number, rounds: number) {
  let seed = seedStart;
  const rand = (n: number) => {
    seed = (seed * 1103515245 + 12345) % 2147483648;
    return seed % n;
  };
  const words = ["alpha", "beta", "gamma", "*delta*", "`eps`", "**zeta**", "[eta](x.md)"];
  const { update, sourceMap } = serverDoc("# Doc\n\nOne.\n\nTwo.\n\nThree.\n\n- a\n- b\n");
  const clients = [0, 1, 2].map((i) => {
    const d = new Y.Doc();
    d.clientID = 100 + i; // deterministic merges
    Y.applyUpdate(d, update);
    return new SourceSync(d, sourceMap);
  });
  const queues: Uint8Array[][] = clients.map(() => []);
  clients.forEach((c, i) =>
    c.doc.on("update", (u: Uint8Array, o: unknown) => {
      if (o === "remote") return;
      clients.forEach((_, j) => j !== i && queues[j]!.push(u));
    }),
  );
  const deliver = (j: number) => {
    for (const u of queues[j]!.splice(0)) Y.applyUpdate(clients[j]!.doc, u, "remote");
  };
  for (let round = 0; round < rounds; round++) {
    const c = clients[rand(3)]!;
    const paras = c.text().split("\n\n");
    const k = rand(paras.length);
    const op = rand(4);
    if (op === 0) paras[k] = paras[k] + " " + words[rand(words.length)];
    else if (op === 1) paras.splice(k + 1, 0, words[rand(words.length)] + " para " + round);
    else if (op === 2 && paras.length > 3) paras.splice(k, 1);
    else paras[k] = paras[k]!.replace(/\w+/, (w) => w.toUpperCase());
    c.apply(paras.join("\n\n"));
    if (rand(2)) deliver(rand(3));
  }
  [0, 1, 2].forEach(deliver);
  return clients;
}

const model = (text: string) => canonical(parse(text).doc.content.map((b) => ({ ...b, attrs: { ...(b as { attrs?: object }).attrs, sid: undefined } })));

test.each([...Array(Number(process.env.FUZZ_SEEDS ?? 40)).keys()].map((i) => [i + 1]))("three concurrent source editors converge (seed %i)", (seedStart) => {
  const clients = fuzz(seedStart, 150);
  const texts = clients.map((c) => c.text());
  expect(model(texts[1]!)).toBe(model(texts[0]!));
  expect(model(texts[2]!)).toBe(model(texts[0]!));
  for (const c of clients) {
    const before = Y.encodeStateVector(c.doc);
    c.apply(c.text());
    expect(Y.encodeStateVector(c.doc)).toEqual(before);
  }
});
