import * as Y from "yjs";
import { nodeHash } from "./hash";
import { parse } from "./parse";
import { serialize } from "./serialize";
import type { BlockNode, SourceMap } from "./schema";
import { CONTENT, applyDoc, readDoc } from "./ydoc";

/**
 * Keeps a markdown text (source mode) and a collaborative document in sync.
 *
 * The document stores the model, not bytes, so a naive re-serialization after
 * a collaborator's edit would reformat everything the user typed. Instead,
 * blocks keep the exact bytes they had in the last text this client applied:
 * each Y element is tied to its block in that text, and serialization reuses
 * those bytes while the block's content is unchanged. Only blocks someone else
 * changed are re-serialized. Before any local edit, the server's source map
 * (the text the document was created from) plays that role.
 *
 * See docs/specs/14-roadmap.md (spike S3) and docs/specs/05-collaboration.md.
 */
export class SourceSync {
  readonly frag: Y.XmlFragment;
  /** Transaction origin of local applies (to tell them apart from remote updates). */
  readonly origin = { source: true };
  private local: SourceMap | null = null;
  private owners = new WeakMap<Y.AbstractType<unknown>, number>();

  constructor(
    readonly doc: Y.Doc,
    private readonly base?: SourceMap,
  ) {
    this.frag = doc.getXmlFragment(CONTENT);
  }

  /** The document as markdown, keeping the bytes of blocks nobody changed. */
  text(): string {
    const d = readDoc(this.frag);
    if (!this.local) return serialize(d, this.base);
    const els = this.frag.toArray();
    d.content = d.content.map((b, i) => {
      const sid = els[i] ? this.owners.get(els[i] as Y.AbstractType<unknown>) : undefined;
      return { ...b, attrs: { ...((b as { attrs?: object }).attrs ?? {}), sid } } as BlockNode;
    });
    return serialize(d, this.local);
  }

  /** Applies edited source text to the document as a local change. */
  apply(text: string): void {
    const parsed = parse(text);
    this.doc.transact(() => applyDoc(this.frag, parsed.doc, nodeHash), this.origin);
    // The fragment now matches the parsed blocks one for one.
    this.local = parsed.sourceMap;
    this.owners = new WeakMap();
    const els = this.frag.toArray();
    parsed.doc.content.forEach((b, i) => {
      const sid = (b as { attrs?: { sid?: number } }).attrs?.sid;
      if (els[i] && sid !== undefined) this.owners.set(els[i] as Y.AbstractType<unknown>, sid);
    });
  }
}

/** The smallest single change turning a into b (common prefix and suffix kept). */
export function textChange(a: string, b: string): { from: number; to: number; insert: string } | null {
  if (a === b) return null;
  let pre = 0;
  const max = Math.min(a.length, b.length);
  while (pre < max && a.charCodeAt(pre) === b.charCodeAt(pre)) pre++;
  let suf = 0;
  while (suf < max - pre && a.charCodeAt(a.length - 1 - suf) === b.charCodeAt(b.length - 1 - suf)) suf++;
  return { from: pre, to: a.length - suf, insert: b.slice(pre, b.length - suf) };
}
