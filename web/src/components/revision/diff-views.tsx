import { Fragment, useMemo } from "react";
import { useTranslation } from "react-i18next";
import { nodeHash, parse, serializeBlock, type BlockNode } from "@kmdn/doc-engine";
import { DocView, type DocContext } from "@/components/doc/doc-view";
import type { components } from "@kmdn/api-client";
import { cn } from "@/lib/utils";

type Hunk = components["schemas"]["DiffHunk"];

function lcs(a: string[], b: string[]): [number, number][] {
  const dp: Uint32Array[] = Array.from({ length: a.length + 1 }, () => new Uint32Array(b.length + 1));
  for (let i = a.length - 1; i >= 0; i--) for (let j = b.length - 1; j >= 0; j--) dp[i]![j] = a[i] === b[j] ? dp[i + 1]![j + 1]! + 1 : Math.max(dp[i + 1]![j]!, dp[i]![j + 1]!);
  const out: [number, number][] = [];
  let i = 0;
  let j = 0;
  while (i < a.length && j < b.length) {
    if (a[i] === b[j]) {
      out.push([i, j]);
      i++;
      j++;
    } else if (dp[i + 1]![j]! >= dp[i]![j + 1]!) i++;
    else j++;
  }
  return out;
}

/** Word-level diff of two strings (whitespace-separated tokens kept). */
function wordDiff(a: string, b: string): { op: "=" | "+" | "-"; text: string }[] {
  const ta = a.split(/(\s+)/);
  const tb = b.split(/(\s+)/);
  const pairs = lcs(ta, tb);
  const out: { op: "=" | "+" | "-"; text: string }[] = [];
  let i = 0;
  let j = 0;
  const push = (op: "=" | "+" | "-", text: string) => {
    const last = out[out.length - 1];
    if (last && last.op === op) last.text += text;
    else out.push({ op, text });
  };
  for (const [pi, pj] of [...pairs, [ta.length, tb.length] as [number, number]]) {
    while (i < pi) push("-", ta[i++]!);
    while (j < pj) push("+", tb[j++]!);
    if (pi < ta.length) push("=", ta[pi]!);
    i = pi + 1;
    j = pj + 1;
  }
  return out;
}

const plain = (b: BlockNode): string => {
  const text = (n: unknown): string => {
    const x = n as { text?: string; content?: unknown[] };
    return (x.text ?? "") + (x.content ?? []).map(text).join("");
  };
  return text(b);
};

function plainBlock(b: BlockNode): boolean {
  return (b.type === "paragraph" || b.type === "heading") && (b.content ?? []).every((n) => n.type === "text" && !n.marks?.length);
}

type ReviewNode = { type: string; attrs?: Record<string, unknown>; content?: ReviewNode[]; marks?: ReviewNode[] };

/** Individual diff blocks cannot rely on reference definitions in other blocks. */
function inlineReferences(node: ReviewNode): ReviewNode {
  return {
    ...node,
    ...(node.attrs?.ref ? { attrs: { ...node.attrs, ref: undefined } } : {}),
    ...(node.content ? { content: node.content.map(inlineReferences) } : {}),
    ...(node.marks ? { marks: node.marks.map(inlineReferences) } : {}),
  };
}

/** Destinations and descriptions can change without changing the rendered label. */
function reviewDetails(b: BlockNode): string[] {
  const values = new Set<string>();
  const walk = (value: unknown) => {
    const node = value as { type?: string; attrs?: Record<string, unknown>; marks?: unknown[]; content?: unknown[] };
    const fields = node.type === "link" ? ["href", "title"] : node.type === "image" ? ["src", "alt", "title"] : node.type === "mathInline" ? ["source"] : [];
    for (const field of fields) {
      const text = node.attrs?.[field];
      if (typeof text === "string" && text) values.add(text);
    }
    for (const child of [...(node.content ?? []), ...(node.marks ?? [])]) walk(child);
  };
  walk(b);
  return [...values];
}

/**
 * Changes view: the page as a rendered diff. Unchanged blocks render
 * normally; a rewritten paragraph shows word-level insertions and deletions;
 * other changes show the old block struck through and the new one
 * highlighted. Read-only.
 */
export function ChangesView({ base, content, ctx }: { base: string; content: string; ctx: DocContext }) {
  const { t } = useTranslation();
  const parts = useMemo(() => {
    const a = parse(base).doc.content;
    const b = parse(content).doc.content;
    const pairs = lcs(
      a.map((x) => nodeHash(x)),
      b.map((x) => nodeHash(x)),
    );
    const out: { kind: "same" | "changed" | "added" | "removed"; old?: BlockNode; now?: BlockNode }[] = [];
    let i = 0;
    let j = 0;
    for (const [pi, pj] of [...pairs, [a.length, b.length] as [number, number]]) {
      const olds = a.slice(i, pi);
      const news = b.slice(j, pj);
      const n = Math.max(olds.length, news.length);
      for (let k = 0; k < n; k++) {
        const o = olds[k];
        const w = news[k];
        if (o && w && o.type === w.type && plainBlock(o) && plainBlock(w) && (o.type !== "heading" || (w.type === "heading" && o.attrs.level === w.attrs.level))) out.push({ kind: "changed", old: o, now: w });
        else {
          if (o) out.push({ kind: "removed", old: o });
          if (w) out.push({ kind: "added", now: w });
        }
      }
      if (pj < b.length) out.push({ kind: "same", now: b[pj] });
      i = pi + 1;
      j = pj + 1;
    }
    return out;
  }, [base, content]);
  const md = (b: BlockNode) => serializeBlock(inlineReferences(b) as BlockNode) + "\n";
  if (!parts.some((p) => p.kind !== "same")) return <p className="doc text-muted-foreground">{t("diff.noChanges")}</p>;
  return (
    <div className="doc changes-view" aria-label={t("diff.changes")}>
      {parts.map((p, i) => {
        switch (p.kind) {
          case "same":
            return <DocView key={i} markdown={md(p.now!)} ctx={ctx} className="!m-0 !max-w-none !p-0" />;
          case "added":
            return (
              <div key={i} className="chg-added">
                <DocView markdown={md(p.now!)} ctx={ctx} className="!m-0 !max-w-none !p-0" />
                <AttributeDetails block={p.now!} />
              </div>
            );
          case "removed":
            return (
              <div key={i} className="chg-removed" aria-label={t("diff.removed")}>
                <DocView markdown={md(p.old!)} ctx={ctx} className="!m-0 !max-w-none !p-0" />
                <AttributeDetails block={p.old!} />
              </div>
            );
          case "changed": {
            const Tag = p.now!.type === "heading" ? (`h${(p.now as { attrs: { level: number } }).attrs.level}` as "h2") : "p";
            return (
              <Tag key={i} className="chg-changed">
                {wordDiff(plain(p.old!), plain(p.now!)).map((w, k) =>
                  w.op === "=" ? <Fragment key={k}>{w.text}</Fragment> : w.op === "+" ? <ins key={k}>{w.text}</ins> : <del key={k}>{w.text}</del>,
                )}
              </Tag>
            );
          }
        }
      })}
    </div>
  );
}

function AttributeDetails({ block }: { block: BlockNode }) {
  const details = reviewDetails(block);
  if (!details.length) return null;
  return <ul className="my-2 break-all font-mono text-xs text-muted-foreground">{details.map((value) => <li key={value}>{value}</li>)}</ul>;
}

/** Source diff: unified markdown line diff with line numbers. */
export function SourceDiff({ hunks }: { hunks: Hunk[] }) {
  const { t } = useTranslation();
  if (hunks.length === 0) return <p className="doc text-muted-foreground">{t("diff.noChanges")}</p>;
  return (
    <div className="mx-auto my-6 max-w-[60rem] overflow-hidden rounded-xl border font-mono text-[0.78125rem] max-md:mx-2">
      {hunks.map((h, i) => (
        <div key={i}>
          <div className="border-y bg-muted px-3 py-1 text-muted-foreground first:border-t-0">
            @@ −{h.old_start},{h.old_lines} +{h.new_start},{h.new_lines} @@
          </div>
          <table className="w-full border-collapse">
            <tbody>
              {h.lines.map((l, k) => (
                <tr key={k} className={cn(l.op === "+" && "bg-success/10", l.op === "-" && "bg-destructive/10")}>
                  <td className="w-10 px-2 text-right align-top text-muted-foreground select-none">{l.old ?? ""}</td>
                  <td className="w-10 px-2 text-right align-top text-muted-foreground select-none">{l.new ?? ""}</td>
                  <td className="w-4 align-top select-none">{l.op === " " ? "" : l.op}</td>
                  <td className="px-2 break-all whitespace-pre-wrap">{l.text || " "}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ))}
    </div>
  );
}
