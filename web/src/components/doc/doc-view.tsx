import { Fragment, useEffect, useId, useMemo, useRef, useState, type ReactNode } from "react";
import { useRouter } from "@tanstack/react-router";
import DOMPurify from "dompurify";
import katex from "katex";
import "katex/dist/katex.min.css";
import { Check, Copy, Braces, Info, Lightbulb, AlertTriangle, OctagonAlert, MessageSquareWarning } from "lucide-react";
import { parse, type BlockNode, type InlineNode, type Mark } from "@kmdn/doc-engine";
import { useTheme } from "@/lib/theme";
import { isExternal, resolveRelative, slugify } from "@/lib/paths";
import { cn } from "@/lib/utils";
import { parseAlert, parseContainer } from "@/lib/callouts";

export type DocContext = {
  /** Repo-relative path of the file being shown. */
  path: string;
  /** Maps a repo-relative path to an app route (for links) or a URL (for images). */
  pageHref: (path: string) => string;
  imageSrc: (path: string) => string;
};

/** Line range (1-based, inclusive) of a top-level block in the source. */
export type BlockLines = { start: number; end: number };

/** Renders published markdown with the shared doc engine. `aside` renders an
 * annotation next to each top-level block (used by blame). */
export function DocView({ markdown, ctx, className, aside }: { markdown: string; ctx: DocContext; className?: string; aside?: (i: number, lines: BlockLines) => ReactNode }) {
  const { doc, sourceMap } = useMemo(() => parse(markdown), [markdown]);
  const lines = useMemo(() => {
    const starts: number[] = [0];
    const src = sourceMap.original;
    for (let i = 0; i < src.length; i++) if (src.charCodeAt(i) === 10) starts.push(i + 1);
    const lineOf = (off: number) => {
      let lo = 0;
      let hi = starts.length - 1;
      while (lo < hi) {
        const mid = (lo + hi + 1) >> 1;
        if (starts[mid]! <= off) lo = mid;
        else hi = mid - 1;
      }
      return lo + 1;
    };
    return sourceMap.blocks.map((b) => ({ start: lineOf(b.start), end: lineOf(Math.max(b.start, b.end - 1)) }));
  }, [sourceMap]);
  const slugs = new Map<string, number>();
  const headingId = (text: string) => {
    const base = slugify(text) || "section";
    const n = slugs.get(base) ?? 0;
    slugs.set(base, n + 1);
    return n ? `${base}-${n}` : base;
  };
  const r = new Renderer(ctx, headingId);
  if (!aside) return <article className={cn("doc", className)}>{doc.content.map((b, i) => r.block(b, i))}</article>;
  return (
    <article className={cn("doc doc-annotated", className)}>
      {doc.content.map((b, i) => (
        <div key={i} className="annotated-block">
          <div className="annotation">{lines[i] && aside(i, lines[i]!)}</div>
          <div className="min-w-0">{r.block(b, i)}</div>
        </div>
      ))}
    </article>
  );
}

function plainText(nodes: InlineNode[] | undefined): string {
  return (nodes ?? []).map((n) => (n.type === "text" ? n.text : n.type === "image" ? n.attrs.alt : n.type === "mathInline" ? n.attrs.source : "")).join("");
}

const ALERTS: Record<string, { icon: typeof Info; label: string; tone: string }> = {
  note: { icon: Info, label: "Note", tone: "border-info/40 bg-info/5 [&_.callout-t]:text-info" },
  tip: { icon: Lightbulb, label: "Tip", tone: "border-success/40 bg-success/5 [&_.callout-t]:text-success" },
  important: { icon: MessageSquareWarning, label: "Important", tone: "border-violet-500/40 bg-violet-500/5 [&_.callout-t]:text-violet-600 dark:[&_.callout-t]:text-violet-400" },
  warning: { icon: AlertTriangle, label: "Warning", tone: "border-warning/40 bg-warning/5 [&_.callout-t]:text-warning" },
  caution: { icon: OctagonAlert, label: "Caution", tone: "border-destructive/40 bg-destructive/5 [&_.callout-t]:text-destructive" },
  danger: { icon: OctagonAlert, label: "Danger", tone: "border-destructive/40 bg-destructive/5 [&_.callout-t]:text-destructive" },
  info: { icon: Info, label: "Info", tone: "border-info/40 bg-info/5 [&_.callout-t]:text-info" },
};

class Renderer {
  constructor(
    private ctx: DocContext,
    private headingId: (t: string) => string,
  ) {}

  block(b: BlockNode, key: number | string): ReactNode {
    switch (b.type) {
      case "frontmatter":
        return <Properties key={key} value={b.attrs.value} format={b.attrs.format} />;
      case "paragraph":
        return <p key={key}>{this.inlines(b.content)}</p>;
      case "heading": {
        const text = plainText(b.content);
        const id = this.headingId(text);
        const H = `h${Math.min(b.attrs.level, 6)}` as "h1";
        return (
          <H key={key} id={id} className="group scroll-mt-20">
            {this.inlines(b.content)}
            {b.attrs.level > 1 && (
              <a href={`#${id}`} className="ml-2 text-muted-foreground opacity-0 transition-opacity group-hover:opacity-100" aria-label={`Link to ${text}`}>
                #
              </a>
            )}
          </H>
        );
      }
      case "blockquote":
        return <blockquote key={key}>{b.content.map((c, i) => this.block(c, i))}</blockquote>;
      case "bulletList":
      case "orderedList": {
        const task = b.content.some((li) => li.attrs.checked !== null);
        const items = b.content.map((li, i) => (
          <li key={i} className={cn(li.attrs.checked !== null && "flex gap-2.5", li.attrs.checked === true && "done")}>
            {li.attrs.checked !== null && (
              <span aria-hidden className={cn("mt-1.5 grid size-4 shrink-0 place-items-center rounded border border-primary", li.attrs.checked && "bg-primary text-primary-foreground")}>
                {li.attrs.checked && <Check className="size-3" strokeWidth={3} />}
              </span>
            )}
            <div className="min-w-0 flex-1">{li.content.map((c, j) => this.listChild(c, j, b.attrs.spread))}</div>
          </li>
        ));
        return b.type === "orderedList" ? (
          <ol key={key} start={b.attrs.start}>
            {items}
          </ol>
        ) : (
          <ul key={key} className={cn(task && "tasks")}>
            {items}
          </ul>
        );
      }
      case "codeBlock": {
        const code = (b.content ?? []).map((t) => t.text).join("");
        if (b.attrs.lang === "mermaid") return <Mermaid key={key} source={code} />;
        return <CodeBlock key={key} lang={b.attrs.lang} code={code} />;
      }
      case "mathBlock":
        return <MathView key={key} source={(b.content ?? []).map((t) => t.text).join("")} display />;
      case "table":
        return (
          <div key={key} className="table-wrap">
            <table>
              <thead>
                {b.content.slice(0, 1).map((row, i) => (
                  <tr key={i}>
                    {row.content.map((c, j) => (
                      <th key={j} style={{ textAlign: b.attrs.align[j] ?? undefined }}>
                        {this.inlines(c.content)}
                      </th>
                    ))}
                  </tr>
                ))}
              </thead>
              <tbody>
                {b.content.slice(1).map((row, i) => (
                  <tr key={i}>
                    {row.content.map((c, j) => (
                      <td key={j} style={{ textAlign: b.attrs.align[j] ?? undefined }}>
                        {this.inlines(c.content)}
                      </td>
                    ))}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        );
      case "horizontalRule":
        return <hr key={key} />;
      case "footnoteDefinition":
        return (
          <div key={key} id={`fn-${b.attrs.label}`} className="footnote">
            <sup>{b.attrs.label}</sup>
            <div>{b.content.map((c, i) => this.block(c, i))}</div>
          </div>
        );
      case "rawBlock":
        return this.raw(b.attrs.raw, b.attrs.kind, key);
    }
  }

  private listChild(c: BlockNode, key: number, spread: boolean): ReactNode {
    // Tight lists render paragraphs without margins.
    if (c.type === "paragraph" && !spread) return <Fragment key={key}>{this.inlines(c.content)}</Fragment>;
    return this.block(c, key);
  }

  private raw(raw: string, kind: string, key: number | string): ReactNode {
    if (kind === "html") {
      const clean = DOMPurify.sanitize(raw, { USE_PROFILES: { html: true }, FORBID_TAGS: ["style", "form", "input"], FORBID_ATTR: ["style"] });
      return <div key={key} className="raw-html" dangerouslySetInnerHTML={{ __html: clean }} />;
    }
    if (kind === "alert" || kind === "container") {
      const c = kind === "alert" ? parseAlert(raw) : parseContainer(raw);
      return this.callout(key, c.type, c.title, c.body);
    }
    return (
      <div key={key} className="rawblk">
        <div className="rawblk-h">
          <Braces className="size-3.5" />
          Raw markdown
          <span className="rounded-md border px-1.5 text-[0.6875rem]">{kind === "shortcode" ? "shortcode" : kind}</span>
        </div>
        <pre>{raw}</pre>
      </div>
    );
  }

  private callout(key: number | string, type: string, title: string | undefined, body: string): ReactNode {
    const a = ALERTS[type] ?? ALERTS.note!;
    const Icon = a.icon;
    const inner = parse(body).doc.content;
    return (
      <div key={key} className={cn("callout", a.tone)} role="note">
        <div className="callout-t">
          <Icon className="size-4" />
          {title ?? a.label}
        </div>
        {inner.map((c, i) => this.block(c, i))}
      </div>
    );
  }

  inlines(nodes: InlineNode[] | undefined): ReactNode {
    return (nodes ?? []).map((n, i) => this.inline(n, i));
  }

  private inline(n: InlineNode, key: number): ReactNode {
    switch (n.type) {
      case "text":
        return this.marked(n.text, n.marks ?? [], key);
      case "hardBreak":
        return <br key={key} />;
      case "image": {
        const src = isExternal(n.attrs.src) ? n.attrs.src : this.ctx.imageSrc(resolveRelative(this.ctx.path, n.attrs.src.split("#")[0]!));
        return <img key={key} src={src} alt={n.attrs.alt} title={n.attrs.title ?? undefined} loading="lazy" />;
      }
      case "footnoteReference":
        return (
          <sup key={key} className="fn">
            <a href={`#fn-${n.attrs.label}`}>{n.attrs.label}</a>
          </sup>
        );
      case "mathInline":
        return <MathView key={key} source={n.attrs.source} />;
      case "rawInline": {
        const raw = n.attrs.raw;
        if (raw.trimStart().startsWith("<")) {
          const clean = DOMPurify.sanitize(raw, { FORBID_TAGS: ["style", "form", "input"], FORBID_ATTR: ["style"] });
          return <span key={key} dangerouslySetInnerHTML={{ __html: clean }} />;
        }
        return <Fragment key={key}>{raw}</Fragment>;
      }
    }
  }

  private marked(text: string, marks: Mark[], key: number): ReactNode {
    let el: ReactNode = text;
    if (marks.some((m) => m.type === "code")) el = <code>{el}</code>;
    if (marks.some((m) => m.type === "strike")) el = <del>{el}</del>;
    if (marks.some((m) => m.type === "italic")) el = <em>{el}</em>;
    if (marks.some((m) => m.type === "bold")) el = <strong>{el}</strong>;
    const link = marks.find((m) => m.type === "link");
    if (link && link.type === "link") el = this.link(link.attrs.href, link.attrs.title, el);
    return <Fragment key={key}>{el}</Fragment>;
  }

  private link(href: string, title: string | null, children: ReactNode): ReactNode {
    if (href.startsWith("#")) {
      return (
        <a href={href} title={title ?? undefined}>
          {children}
        </a>
      );
    }
    if (isExternal(href)) {
      if (/^javascript:/i.test(href)) return children;
      return (
        <a href={href} title={title ?? undefined} target="_blank" rel="noopener noreferrer">
          {children}
        </a>
      );
    }
    const [p, hash] = href.split("#");
    let decoded = p;
    try {
      decoded = p ? decodeURIComponent(p) : p;
    } catch {
      // Repository filenames can contain literal percent signs.
    }
    const target = decoded ? resolveRelative(this.ctx.path, decoded) : this.ctx.path;
    return (
      <AppLink href={this.ctx.pageHref(target) + (hash ? `#${hash}` : "")} title={title ?? undefined}>
        {children}
      </AppLink>
    );
  }
}

/** In-app link that navigates without a page load (modifier clicks behave normally). */
function AppLink({ href, title, children }: { href: string; title?: string; children: ReactNode }) {
  const router = useRouter();
  return (
    <a
      href={href}
      title={title}
      onClick={(e) => {
        if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
        e.preventDefault();
        router.history.push(href);
      }}
    >
      {children}
    </a>
  );
}

function CodeBlock({ lang, code }: { lang: string | null; code: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <div className="codeblk">
      <div className="codeblk-h">
        <span>{lang ?? "text"}</span>
        <button
          type="button"
          className="inline-flex items-center gap-1 rounded px-1.5 py-0.5 hover:bg-accent"
          onClick={() => {
            void navigator.clipboard?.writeText(code).then(() => {
              setCopied(true);
              setTimeout(() => setCopied(false), 1500);
            });
          }}
        >
          {copied ? <Check className="size-3.5" /> : <Copy className="size-3.5" />}
          {copied ? "Copied" : "Copy"}
        </button>
      </div>
      <pre>
        <code>{code}</code>
      </pre>
    </div>
  );
}

export function MathView({ source, display }: { source: string; display?: boolean }) {
  const html = useMemo(() => {
    try {
      return katex.renderToString(source, { displayMode: !!display, throwOnError: false, strict: "ignore", trust: false });
    } catch {
      return null;
    }
  }, [source, display]);
  if (html === null) return <code>{source}</code>;
  const Tag = display ? "div" : "span";
  return <Tag className={display ? "math-block" : undefined} dangerouslySetInnerHTML={{ __html: html }} />;
}

function Mermaid({ source }: { source: string }) {
  const ref = useRef<HTMLDivElement>(null);
  const id = "mmd" + useId().replace(/[^a-zA-Z0-9]/g, "");
  const { resolved } = useTheme();
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    let cancelled = false;
    void import("mermaid").then(async ({ default: mermaid }) => {
      mermaid.initialize({ startOnLoad: false, securityLevel: "strict", theme: resolved === "dark" ? "dark" : "neutral", fontFamily: "inherit" });
      try {
        const { svg } = await mermaid.render(id, source);
        if (!cancelled && ref.current) ref.current.innerHTML = svg;
        if (!cancelled) setError(null);
      } catch (e) {
        if (!cancelled) setError(e instanceof Error ? e.message : String(e));
      }
    });
    return () => {
      cancelled = true;
    };
  }, [id, source, resolved]);
  return (
    <div className="mermaid-blk">
      {error ? (
        <>
          <p className="px-3 pt-2 text-xs text-destructive">Diagram error: {error}</p>
          <pre className="px-3 pb-3 text-[0.8125rem]">{source}</pre>
        </>
      ) : (
        <div ref={ref} className="flex justify-center p-3.5" role="img" aria-label="Diagram" />
      )}
    </div>
  );
}

/** Frontmatter as a read-only properties panel (editable in the editor, M2). */
function Properties({ value, format }: { value: string; format: string }) {
  const rows = useMemo(() => {
    if (format !== "yaml") return null;
    const out: { k: string; v: string[] | string }[] = [];
    for (const line of value.split("\n")) {
      const m = line.match(/^([A-Za-z0-9_-]+):\s*(.*)$/);
      if (!m) {
        if (line.trim() && !line.startsWith(" ") && !line.startsWith("-")) return null;
        continue;
      }
      const raw = m[2]!.trim();
      if (raw.startsWith("[") && raw.endsWith("]")) out.push({ k: m[1]!, v: raw.slice(1, -1).split(",").map((s) => s.trim().replace(/^["']|["']$/g, "")).filter(Boolean) });
      else out.push({ k: m[1]!, v: raw.replace(/^["']|["']$/g, "") });
    }
    return out;
  }, [value, format]);
  return (
    <div className="props">
      <div className="props-head">
        Properties <span className="font-normal">· {rows ? rows.length : "frontmatter"}</span>
        <span className="ml-auto font-mono text-[0.71875rem]">{format}</span>
      </div>
      {rows ? (
        rows.map((r) => (
          <div key={r.k} className="props-row">
            <div className="text-[0.8125rem] text-muted-foreground">{r.k}</div>
            <div className="flex min-w-0 flex-wrap gap-1.5">
              {Array.isArray(r.v) ? r.v.map((t) => <span key={t} className="tagchip">{t}</span>) : <span className="min-w-0 break-words">{r.v || "—"}</span>}
            </div>
          </div>
        ))
      ) : (
        <pre className="px-3 py-2 text-[0.8125rem]">{value}</pre>
      )}
    </div>
  );
}
