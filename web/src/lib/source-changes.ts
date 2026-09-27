import { ChangeSet, type ChangeSpec } from "@codemirror/state";
import { textChange } from "@kmdn/doc-engine";

const MAX_DISTANCE = 512;
const MAX_WORK = 4_000_000;
const unsafeMerge = () => new Error("These remote changes could not be combined safely. Copy your source, then reload before saving.");

/** Keep unchanged ranges as anchors when mapping edits from another writer. */
export function sourceChanges(before: string, after: string): ChangeSet {
  const outer = textChange(before, after);
  if (!outer) return ChangeSet.empty(before.length);
  const a = before.slice(outer.from, outer.to);
  const b = outer.insert;
  if (!a.length || !b.length) return ChangeSet.of(outer, before.length);

  // Myers' shortest edit path. Bound both search and trace memory; replacing
  // the whole range on exhaustion would move independent local edits.
  const limit = Math.min(MAX_DISTANCE, a.length + b.length);
  const offset = limit + 1;
  const frontier = new Int32Array(2 * limit + 3);
  const trace: Int32Array[] = [];
  let work = 0;
  for (let distance = 0; distance <= limit; distance++) {
    trace.push(frontier.slice());
    for (let diagonal = -distance; diagonal <= distance; diagonal += 2) {
      if (++work > MAX_WORK) throw unsafeMerge();
      const down = diagonal === -distance || (diagonal !== distance && frontier[offset + diagonal - 1]! < frontier[offset + diagonal + 1]!);
      let x = down ? frontier[offset + diagonal + 1]! : frontier[offset + diagonal - 1]! + 1;
      let y = x - diagonal;
      while (x < a.length && y < b.length && a[x] === b[y]) {
        if (++work > MAX_WORK) throw unsafeMerge();
        x++;
        y++;
      }
      frontier[offset + diagonal] = x;
      if (x >= a.length && y >= b.length) return editPath(trace, a.length, b, offset, outer.from, before.length);
    }
  }
  throw unsafeMerge();
}

function editPath(trace: Int32Array[], x: number, after: string, offset: number, start: number, length: number): ChangeSet {
  let y = after.length;
  const edits: ChangeSpec[] = [];
  for (let distance = trace.length - 1; distance > 0; distance--) {
    const frontier = trace[distance]!;
    const diagonal = x - y;
    const down = diagonal === -distance || (diagonal !== distance && frontier[offset + diagonal - 1]! < frontier[offset + diagonal + 1]!);
    const previous = down ? diagonal + 1 : diagonal - 1;
    const previousX = frontier[offset + previous]!;
    const previousY = previousX - previous;
    edits.push(down
      ? { from: start + previousX, insert: after[previousY]! }
      : { from: start + previousX, to: start + previousX + 1 });
    x = previousX;
    y = previousY;
  }
  return ChangeSet.of(edits.reverse(), length);
}
