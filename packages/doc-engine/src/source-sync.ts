import * as Y from "yjs";
import { nodeHash } from "./hash";
import { parse } from "./parse";
import type { SourceMap } from "./schema";
import { CONTENT, applyDoc } from "./ydoc";
import { checkSourceSize, serializeShared, sourceSnapshot, writeSource } from "./shared-source";

/**
 * Keeps a markdown text (source mode) and a collaborative document in sync.
 *
 * Authored block bytes are shared alongside the tree. Each record belongs to
 * one stable Y element and is reused only after its parsed semantics match.
 * The server's source map remains the fallback for older documents.
 *
 * See docs/specs/14-roadmap.md (spike S3) and docs/specs/05-collaboration.md.
 */
export class SourceSync {
  readonly frag: Y.XmlFragment;
  /** Transaction origin of local applies (to tell them apart from remote updates). */
  readonly origin = { source: true };

  constructor(
    readonly doc: Y.Doc,
    private readonly base?: SourceMap,
  ) {
    this.frag = doc.getXmlFragment(CONTENT);
  }

  /** The document as markdown, keeping the bytes of blocks nobody changed. */
  text(): string {
    return serializeShared(this.doc, this.base);
  }

  /** Applies edited source text to the document as a local change. */
  apply(text: string): void {
    checkSourceSize(text);
    const parsed = parse(text);
    const previous = sourceSnapshot(this.doc, parse(this.text()));
    this.doc.transact(() => {
      applyDoc(this.frag, parsed.doc, nodeHash);
      writeSource(this.doc, parsed, previous);
    }, this.origin);
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
