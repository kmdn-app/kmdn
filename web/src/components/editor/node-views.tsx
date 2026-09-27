import { createContext, useContext, useMemo, useState } from "react";
import { NodeViewContent, NodeViewWrapper, ReactNodeViewRenderer, useEditorState, type Editor, type ReactNodeViewProps } from "@tiptap/react";
import { useTranslation } from "react-i18next";
import { Braces, Plus, X } from "lucide-react";
import { isMap, isScalar, isSeq, parseDocument, type Document } from "yaml";
import { DocView, MathView, type DocContext } from "@/components/doc/doc-view";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { Frontmatter, MathBlock, MathInline, RawBlock } from "./schema";

/** Page context for previews inside node views (links, images). */
export const EditorDocContext = createContext<DocContext>({ path: "", pageHref: (p) => p, imageSrc: (p) => p });

/** Node views render once; this follows the editor turning editing on and off. */
function useEditable(editor: Editor): boolean {
  return useEditorState({ editor, selector: ({ editor: e }) => e?.isEditable ?? false });
}

/** Commits on blur or Enter, so a whole value is one change for collaborators. */
function CommitInput({ value, onCommit, className, placeholder, disabled, label }: { value: string; onCommit: (v: string) => void; className?: string; placeholder?: string; disabled?: boolean; label: string }) {
  const [draft, setDraft] = useState<string | null>(null);
  return (
    <input
      aria-label={label}
      value={draft ?? value}
      placeholder={placeholder}
      disabled={disabled}
      onChange={(e) => setDraft(e.target.value)}
      onBlur={() => {
        if (draft !== null && draft !== value) onCommit(draft);
        setDraft(null);
      }}
      onKeyDown={(e) => {
        if (e.key === "Enter") (e.target as HTMLInputElement).blur();
        if (e.key === "Escape") {
          setDraft(null);
          (e.target as HTMLInputElement).blur();
        }
      }}
      className={cn("min-w-0 rounded-md border border-transparent bg-transparent px-1.5 py-0.5 hover:border-input focus:border-input focus:bg-background focus:outline-none disabled:hover:border-transparent", className)}
    />
  );
}

type Row = { key: string; kind: "scalar" | "list" | "other"; text: string; items: string[] };

function rowsOf(doc: Document): Row[] | null {
  if (doc.errors.length || !isMap(doc.contents)) return doc.contents === null ? [] : null;
  return doc.contents.items.map((pair) => {
    const key = isScalar(pair.key) ? String(pair.key.value) : String(pair.key);
    const v = pair.value;
    if (v === null || isScalar(v)) return { key, kind: "scalar", text: v === null || v.value === null ? "" : String(v.value), items: [] };
    if (isSeq(v) && v.items.every((i) => isScalar(i))) return { key, kind: "list", text: "", items: v.items.map((i) => String((i as { value: unknown }).value)) };
    return { key, kind: "other", text: String(v).trim(), items: [] };
  });
}

/** Front matter as editable properties (the YAML keeps its formatting and comments). */
function FrontmatterView({ node, updateAttributes, editor }: ReactNodeViewProps) {
  const { t } = useTranslation();
  const value = String(node.attrs.value ?? "");
  const format = String(node.attrs.format ?? "yaml");
  const [raw, setRaw] = useState(false);
  const [newKey, setNewKey] = useState("");
  const editable = useEditable(editor);
  const doc = useMemo(() => (format === "yaml" ? parseDocument(value) : null), [value, format]);
  const rows = doc ? rowsOf(doc) : null;
  const write = (fn: (d: Document) => void) => {
    const d = parseDocument(value);
    fn(d);
    updateAttributes({ value: String(d).replace(/\n$/, "") });
  };
  return (
    <NodeViewWrapper className="props" contentEditable={false} data-drag-handle>
      <div className="props-head">
        {t("editor.properties")} <span className="font-normal">· {rows ? rows.length : format}</span>
        <button type="button" className="ml-auto font-mono text-[0.71875rem] hover:text-foreground" onClick={() => setRaw((v) => !v)} aria-pressed={raw}>
          {raw || !rows ? format : t("editor.editYaml")}
        </button>
      </div>
      {raw || !rows ? (
        <textarea
          aria-label={t("editor.frontmatterSource")}
          defaultValue={value}
          disabled={!editable}
          onBlur={(e) => e.target.value !== value && updateAttributes({ value: e.target.value })}
          rows={Math.min(12, value.split("\n").length + 1)}
          className="w-full resize-y bg-transparent px-3 py-2 font-mono text-[0.8125rem] focus:outline-none"
        />
      ) : (
        <>
          {rows.map((r) => (
            <div key={r.key} className="props-row group">
              <div className="text-[0.8125rem] text-muted-foreground">{r.key}</div>
              <div className="flex min-w-0 flex-1 flex-wrap items-center gap-1.5">
                {r.kind === "scalar" && (
                  <CommitInput label={r.key} value={r.text} disabled={!editable} className="w-full" onCommit={(v) => write((d) => d.set(r.key, v))} />
                )}
                {r.kind === "list" && (
                  <>
                    {r.items.map((item, i) => (
                      <span key={i} className="tagchip inline-flex items-center gap-1">
                        {item}
                        {editable && (
                          <button type="button" aria-label={t("editor.removeItem", { item })} onClick={() => write((d) => d.deleteIn([r.key, i]))}>
                            <X className="size-3" />
                          </button>
                        )}
                      </span>
                    ))}
                    {editable && <CommitInput label={t("editor.addItem", { key: r.key })} value="" placeholder="+" className="w-20" onCommit={(v) => v.trim() && write((d) => d.addIn([r.key], v.trim()))} />}
                  </>
                )}
                {r.kind === "other" && <code className="text-[0.75rem] whitespace-pre-wrap">{r.text}</code>}
              </div>
              {editable && (
                <button type="button" className="opacity-0 group-hover:opacity-100" aria-label={t("editor.removeProperty", { key: r.key })} onClick={() => write((d) => d.delete(r.key))}>
                  <X className="size-3.5 text-muted-foreground" />
                </button>
              )}
            </div>
          ))}
          {editable && (
            <div className="props-row">
              <input
                aria-label={t("editor.newProperty")}
                value={newKey}
                onChange={(e) => setNewKey(e.target.value.replace(/[^A-Za-z0-9_-]/g, ""))}
                onKeyDown={(e) => {
                  if (e.key === "Enter" && newKey) {
                    write((d) => d.set(newKey, ""));
                    setNewKey("");
                  }
                }}
                placeholder={t("editor.newProperty")}
                className="min-w-0 bg-transparent px-1.5 text-[0.8125rem] focus:outline-none"
              />
              <Button
                type="button"
                variant="ghost"
                size="sm"
                className="h-6 px-1.5"
                disabled={!newKey}
                onClick={() => {
                  write((d) => d.set(newKey, ""));
                  setNewKey("");
                }}
              >
                <Plus />
              </Button>
            </div>
          )}
        </>
      )}
    </NodeViewWrapper>
  );
}

/** Display math: the TeX is editable in place, with a live preview. */
function MathBlockView({ node }: ReactNodeViewProps) {
  return (
    <NodeViewWrapper className="math-edit">
      <NodeViewContent<"pre"> as="pre" className="math-src" />
      <div contentEditable={false} className="math-preview">
        <MathView source={node.textContent} display />
      </div>
    </NodeViewWrapper>
  );
}

/** Inline math: rendered; click to edit the TeX. */
function MathInlineView({ node, updateAttributes, editor, selected }: ReactNodeViewProps) {
  const [editing, setEditing] = useState(false);
  const editable = useEditable(editor);
  const source = String(node.attrs.source ?? "");
  return (
    <NodeViewWrapper as="span" className={cn("math-inline-edit", selected && "is-selected")} contentEditable={false}>
      {editing ? (
        <CommitInput
          label="TeX"
          value={source}
          className="font-mono text-[0.8125rem]"
          onCommit={(v) => {
            updateAttributes({ source: v });
            setEditing(false);
          }}
        />
      ) : (
        <span role="button" tabIndex={-1} onDoubleClick={() => editable && setEditing(true)} title={source}>
          <MathView source={source} />
        </span>
      )}
    </NodeViewWrapper>
  );
}

/** Markdown kmdn keeps as-is (HTML, alerts, containers, shortcodes): preview, or edit its source. */
function RawBlockView({ node, updateAttributes, editor }: ReactNodeViewProps) {
  const { t } = useTranslation();
  const ctx = useContext(EditorDocContext);
  const [editing, setEditing] = useState(false);
  const editable = useEditable(editor);
  const raw = String(node.attrs.raw ?? "");
  const kind = String(node.attrs.kind ?? "unknown");
  const previewable = kind === "html" || kind === "alert" || kind === "container";
  return (
    <NodeViewWrapper className="rawblk-edit" contentEditable={false} data-drag-handle>
      <div className="rawblk-bar">
        <Braces className="size-3.5" />
        {t(`editor.raw.${kind}`, { defaultValue: t("editor.raw.unknown") })}
        {editable && (
          <button type="button" className="ml-auto hover:text-foreground" onClick={() => setEditing((v) => !v)} aria-pressed={editing}>
            {editing ? t("editor.done") : t("editor.editSource")}
          </button>
        )}
      </div>
      {editing ? (
        <textarea
          aria-label={t("editor.rawSource")}
          defaultValue={raw}
          autoFocus
          onBlur={(e) => e.target.value !== raw && updateAttributes({ raw: e.target.value })}
          rows={Math.min(16, raw.split("\n").length + 1)}
          className="w-full resize-y bg-transparent px-3 py-2 font-mono text-[0.8125rem] focus:outline-none"
        />
      ) : previewable ? (
        <DocView markdown={raw} ctx={ctx} className="!m-0 !max-w-none !p-3" />
      ) : (
        <pre className="px-3 py-2 font-mono text-[0.8125rem] whitespace-pre-wrap">{raw}</pre>
      )}
    </NodeViewWrapper>
  );
}

/** The schema's nodes with their React views. */
export const nodeViewExtensions = [
  Frontmatter.extend({ addNodeView: () => ReactNodeViewRenderer(FrontmatterView) }),
  MathBlock.extend({ addNodeView: () => ReactNodeViewRenderer(MathBlockView) }),
  MathInline.extend({ addNodeView: () => ReactNodeViewRenderer(MathInlineView) }),
  RawBlock.extend({ addNodeView: () => ReactNodeViewRenderer(RawBlockView) }),
];
