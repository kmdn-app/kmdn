/**
 * Globals the Go host (goja) lacks and lib0 reads at load time. Imported
 * first by host.ts so it runs before Yjs. Yjs only needs randomness for
 * client ids, so Math.random is enough.
 */
const g = globalThis as unknown as { crypto?: unknown };
if (!g.crypto) {
  g.crypto = {
    subtle: {},
    getRandomValues<T extends ArrayBufferView>(arr: T): T {
      const a = arr as unknown as { length: number; [i: number]: number };
      for (let i = 0; i < a.length; i++) a[i] = Math.floor(Math.random() * 0x100000000);
      return arr;
    },
  };
}
export {};
